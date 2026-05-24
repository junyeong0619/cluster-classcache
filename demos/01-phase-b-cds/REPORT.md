# Phase B Verification Report — Can ByteBuddy transforms be baked into a Dynamic CDS Archive?

**Scope**: the core technical premise of cluster-classcache PoC v0.3.
**Conclusion**: PASS (3/3 critical checks).

---

## 1. Why this check

The v0.3 design depends on this mechanism:

> "One JVM uses an agent to transform classes and bakes the result into a dynamic CDS archive. Other JVMs simply mmap the archive and use the transformed classes **without the agent**."

If this doesn't hold, all of v0.3 collapses. Phase B is the minimal experiment that validates it.

## 2. Three hypotheses to verify

| H | Hypothesis | How we verify |
|---|------|---------|
| H1 | A ByteBuddy-transformed class can be included in the dynamic CDS archive produced by `-XX:ArchiveClassesAtExit` | After Phase 1, archive file exists and the cds log shows "app/unregistered class" counts |
| H2 | When another JVM mmaps that archive via `-XX:SharedArchiveFile`, the transformed App class is loaded from the archive (not the jar) | After Phase 2a, `class+load` log shows `source: shared objects file` |
| H3 | Even though Phase 2a's JVM doesn't run the agent, the transformed code actually executes | Phase 2a output contains the `TRACE-ENTER`/`TRACE-EXIT` lines the advice injects |

All three must PASS for the v0.3 model ("Primer builds the archive, Workload Pods just mmap it without the agent") to hold.

## 3. Test environment

- **OS**: macOS (Darwin 25.5.0)
- **JDK**: OpenJDK 22.0.2+9-70 (HotSpot, mixed mode)
- **ByteBuddy**: 1.14.19 (auto-downloaded from Maven Central)
- **Build tools**: only system `javac`, `jar`, `curl`, `unzip` (no Gradle/Maven — self-contained)

## 4. Code under test

### 4.1 Target App (`src/app/com/example/app/App.java`)
```java
public class App {
    public static void main(String[] args) {
        System.out.println("[APP] start");
        for (int i = 0; i < 3; i++) doWork(i);
        System.out.println("[APP] done");
    }
    public static void doWork(int n) {
        System.out.println("[APP] doWork " + n);
    }
}
```

### 4.2 ByteBuddy Advice (`TraceAdvice.java`)
```java
public class TraceAdvice {
    @Advice.OnMethodEnter
    public static void enter(@Advice.Origin("#m") String method) {
        System.out.println("[TRACE-ENTER] " + method);
    }
    @Advice.OnMethodExit
    public static void exit(@Advice.Origin("#m") String method) {
        System.out.println("[TRACE-EXIT] " + method);
    }
}
```

### 4.3 Agent registration (`TraceAgent.java`)
```java
public static void premain(String args, Instrumentation inst) {
    new AgentBuilder.Default()
        .disableClassFormatChanges()
        .with(AgentBuilder.InitializationStrategy.NoOp.INSTANCE)
        .with(AgentBuilder.TypeStrategy.Default.REDEFINE)
        .with(AgentBuilder.RedefinitionStrategy.DISABLED)
        .type(ElementMatchers.named("com.example.app.App"))
        .transform((builder, type, cl, mod, pd) ->
            builder.visit(Advice.to(TraceAdvice.class)
                .on(ElementMatchers.named("doWork")))
        )
        .installOn(inst);
}
```

Intentionally narrowed to CDS-friendly settings:
- `disableClassFormatChanges()` — avoids stack frame map changes.
- `TypeStrategy.REDEFINE` — redefine existing types (no new class creation).
- `RedefinitionStrategy.DISABLED` — don't re-transform already-loaded classes.
- `Advice.to(...)` — static advice (no runtime code generation).

## 5. Experiment design (3 phases)

### Phase 1 — Archive Build (agent ON)
```bash
java \
  -XX:+UnlockDiagnosticVMOptions \
  -XX:+AllowArchivingWithJavaAgent \
  -XX:ArchiveClassesAtExit=work/app.jsa \
  -javaagent:work/agent.jar \
  -Xlog:cds=info,cds+dynamic=info,cds+class=debug:file=work/phase1.log \
  -cp work/app.jar \
  com.example.app.App
```

**Expectation**: on normal JVM exit, `app.jsa` is created and contains the transformed App class.

### Phase 2a — Archive Use (agent OFF) ← **the key check**
```bash
java \
  -XX:+UnlockDiagnosticVMOptions \
  -XX:+AllowArchivingWithJavaAgent \
  -XX:SharedArchiveFile=work/app.jsa \
  -Xshare:on \
  -Xlog:cds=info,class+load=info:file=work/phase2a.log \
  -cp work/app.jar \
  com.example.app.App
```

- **No `-javaagent`** — reproduces the v0.3 runtime-pod scenario.
- `-Xshare:on` — die immediately on archive failure instead of silently falling back (keeps the test strict).

**Expectation**: TRACE-ENTER prints, and App loads from the archive.

### Phase 2b — Archive + agent ON (informational)
```bash
java \
  -XX:+UnlockDiagnosticVMOptions \
  -XX:+AllowArchivingWithJavaAgent \
  -XX:SharedArchiveFile=work/app.jsa \
  -Xshare:auto \
  -javaagent:work/agent.jar \
  -Xlog:class+load=info:file=work/phase2b.log \
  -cp work/app.jar \
  com.example.app.App
```

Not counted toward PASS/FAIL; just observing how the archive is treated when the agent is registered.

## 6. Results

### 6.1 Phase 1 results (H1)

`app.jsa` created (1.2 MB). cds log excerpt:
```
[0.296s][warning][cds] This archive was created with AllowArchivingWithJavaAgent.
                       It should be used for testing purposes only ...
[0.309s][info][cds] Number of classes 234
[0.309s][info][cds]     instance classes   =   213
[0.309s][info][cds]       boot             =   211
[0.309s][info][cds]       app              =     1
[0.309s][info][cds]       unregistered     =     1
[0.310s][info][cds,dynamic] Written dynamic archive 0x0000000800c74000 - 0x0000000800d9ada8
                            [792 bytes header, 1207720 bytes total]
```

Reading:
- The App class is in the archive (`app = 1`).
- `unregistered = 1` is likely the result of ByteBuddy passing the transformed output through an unregistered classloader (another representation of the transformed App).
- ~250 ByteBuddy classes are excluded as "Unsupported location" — CDS doesn't archive classes loaded out of the agent jar. **This is not a problem** (the runtime Pod doesn't run the agent, so it doesn't need ByteBuddy classes anyway).

**→ H1 PASS**

### 6.2 Phase 2a results (H2, H3)

stdout:
```
[APP] start
[TRACE-ENTER] doWork
[APP] doWork 0
[TRACE-EXIT] doWork
[TRACE-ENTER] doWork
[APP] doWork 1
[TRACE-EXIT] doWork
[TRACE-ENTER] doWork
[APP] doWork 2
[TRACE-EXIT] doWork
[APP] done
```

Key line from `-Xlog:class+load=info`:
```
[0.022s][info][class,load] com.example.app.App source: shared objects file (top)
```

Reading:
- TRACE-ENTER/EXIT each printed 3 times — **advice code ran even though the agent wasn't active**.
  → means the archive contains the transformed (advice-injected) App bytecode.
- App was loaded directly from `shared objects file (top)` — i.e., the dynamic region of the archive (not the jar).
- "(top)" refers to the dynamic CDS archive; "(base)" would mean the static one.

**→ H2 PASS, H3 PASS**

### 6.3 Phase 2b results (informational)

Adding `-javaagent` flipped the App load source back to the jar:
```
[0.244s][info][class,load] com.example.app.App
                           source: /Users/.../work/app.jar
```

Reading:
- Once the agent is registered, the JVM invokes the transformer callback.
- ByteBuddy's re-transform output doesn't byte-match what was baked into the archive (NamingStrategy seed, constant-pool ordering, etc., are non-deterministic).
- The JVM conservatively bypasses the archive and defines new classes from the transformer's result.
- → **Running the agent at the runtime Pod nullifies the archive benefit.**

This is a negative finding but actually good for the v0.3 design: **don't run the agent in the runtime Pod**. One component fewer to worry about.

## 7. PASS summary

| Check | Result |
|------|------|
| Phase 1: archive built + App included (H1) | PASS |
| Phase 2a: App loaded from archive (H2) | PASS |
| Phase 2a: transformed code executed (H3) | PASS |
| Phase 2b: agent ON bypasses archive (informational) | INFO — expected |

**Final verdict: PASS (3/3 critical checks).**

## 8. Impact on the v0.3 design

| Item | Before | After |
|------|--------|--------|
| Agent on Runtime Pod | Run it (mode=runtime) | **Don't run it** |
| Runtime Pod JVM options | `-javaagent + -XX:SharedArchiveFile` | `-XX:SharedArchiveFile` only |
| Runtime overhead | Transformer callback cost | **0** (no transformer invocation) |
| Pod startup time | Agent load + ByteBuddy init | Just mmap |
| Debugging simplicity | Agent log + archive log | Archive log only |

The design is cleaner and operations are simpler. The agent is needed **only at Primer build time**.

## 9. Limitations and required follow-ups

This check is deliberately minimal:

| Limitation | What needs verifying next |
|------|----------------------|
| Transforms one class with one method | Does it still work with real Spring Boot + an in-house APM agent (thousands of classes)? |
| Same JVM process launched once each | **Multiple JVMs mmap'ing the same archive** — measure OS page sharing (`pmap` / `smaps`) |
| Only simple ByteBuddy advice | Do complex transforms (add field, add interface, change super call) also stay archive-friendly? |
| Single macOS box | Same result in a Linux container? |
| `AllowArchivingWithJavaAgent` is a diagnostic flag — not recommended for production | Need a production-safe story (signing, verification, isolation) |

## 10. How to reproduce

```bash
cd benchmark/cds-verify
./cds-verify.sh
```

The script:
1. Downloads ByteBuddy (`lib/byte-buddy-1.14.19.jar`).
2. Compiles App + Agent.
3. Shades agent.jar (includes ByteBuddy).
4. Runs Phase 1 / 2a / 2b in order.
5. Prints the PASS/FAIL verdict and log locations.

Outputs:
- `work/app.jsa` — dynamic CDS archive (1.2 MB)
- `work/phase1.log`, `work/phase2a.log`, `work/phase2b.log` — JVM CDS / class load logs
- Verdict on the console

## 11. Recommended next actions

1. **Spring Boot scale verification** — extend the same `cds-verify` pattern to Spring Boot 3 + a toy in-house APM agent. Whether H1–H3 still pass with thousands of classes is the biggest risk.
2. **Multi-JVM mmap sharing measurement** — on one box, launch N JVMs that mmap `app.jsa` and measure `Shared_Clean` page counts in `pmap -X` / `smaps`. Get empirical numbers for the theoretical savings.
3. **Build the in-house APM agent for real** — Servlet/HTTP entry, JDBC, span model, stdout exporter. Honor the CDS-friendly constraints (the four points in §1.4.2) from day one.
