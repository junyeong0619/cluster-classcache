package main

import (
	"errors"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
//  Test plumbing — swap `run` for a canned-response dispatcher
// ─────────────────────────────────────────────────────────────────────────────

// runResp is one canned response for a matched shell-out.
type runResp struct {
	out string
	err error
}

// fakeRun matches a (command, args...) call against a list of registered
// matchers and returns the first hit. Unmatched calls fail the test — that
// way each test fully specifies the shell-out surface it exercises.
type fakeRun struct {
	t        *testing.T
	matchers []fakeMatcher
	calls    []string
}

type fakeMatcher struct {
	// name is the binary name to match exactly (e.g. "kubectl").
	name string
	// argPrefix, if non-empty, must match a contiguous prefix of args
	// joined by a single space. "" means "any args".
	argPrefix string
	resp      runResp
}

func newFakeRun(t *testing.T) *fakeRun { return &fakeRun{t: t} }

func (f *fakeRun) on(name, argPrefix string, out string, err error) *fakeRun {
	f.matchers = append(f.matchers, fakeMatcher{name, argPrefix, runResp{out, err}})
	return f
}

func (f *fakeRun) install() {
	orig := run
	run = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		f.calls = append(f.calls, name+" "+joined)
		for _, m := range f.matchers {
			if m.name != name {
				continue
			}
			if m.argPrefix != "" && !strings.HasPrefix(joined, m.argPrefix) {
				continue
			}
			return m.resp.out, m.resp.err
		}
		f.t.Fatalf("fakeRun: unexpected call %s %s\nregistered: %#v", name, joined, f.matchers)
		return "", nil
	}
	f.t.Cleanup(func() { run = orig })
}

// ─────────────────────────────────────────────────────────────────────────────
//  Pure parsers
// ─────────────────────────────────────────────────────────────────────────────

func TestParseCPU(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"100m", 100},
		{"1500m", 1500},
		{"0", 0},
		{"1", 1000},
		{"2.5", 2500},
		{"bogus", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseCPU(c.in); got != c.want {
			t.Errorf("parseCPU(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseMem(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"256Mi", 256},
		{"1Gi", 1024},
		{"2Gi", 2048},
		{"512Ki", 0},   // 512 / 1024 = 0
		{"2048Ki", 2},  // 2048 / 1024 = 2
		{"100", 100},   // no suffix → assumed MiB
		{"", 0},
		{"bad", 0},
	}
	for _, c := range cases {
		if got := parseMem(c.in); got != c.want {
			t.Errorf("parseMem(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  accumulateSmaps
// ─────────────────────────────────────────────────────────────────────────────

// Two .jsa regions with a libc mapping wedged between them. Headers use the
// exact format `<addr1>-<addr2> <perms> <off> <dev> <ino>  <path>` that real
// /proc/PID/smaps emits, so the parser's header heuristic is exercised end-
// to-end.
const smapsFixtureJSA = `7f1234000000-7f1234100000 r-xp 00000000 fd:00 12345                /work/extracted/app.jsa
Size:               1024 kB
KernelPageSize:        4 kB
MMUPageSize:           4 kB
Rss:                 800 kB
Pss:                  50 kB
Shared_Clean:        780 kB
Shared_Dirty:          0 kB
Private_Clean:        20 kB
Private_Dirty:         0 kB
Referenced:          800 kB
Anonymous:             0 kB
VmFlags: rd ex mr mw me sd
7f9999000000-7f9999300000 r-xp 00000000 fd:00 99999                /usr/lib/libc.so.6
Size:               2000 kB
Rss:                1500 kB
Pss:                 100 kB
Shared_Clean:       1400 kB
Shared_Dirty:          0 kB
Private_Clean:        80 kB
Private_Dirty:        20 kB
VmFlags: rd ex mr mw me
7f2000000000-7f2000080000 r-xp 00000000 fd:00 12346                /work/extracted/other.jsa
Size:                512 kB
Rss:                 400 kB
Pss:                  25 kB
Shared_Clean:        390 kB
Shared_Dirty:          0 kB
Private_Clean:        10 kB
Private_Dirty:         0 kB
VmFlags: rd ex mr mw me sd
`

func TestAccumulateSmaps_JSARegions(t *testing.T) {
	snap := SavingsSnapshot{}
	accumulateSmaps(smapsFixtureJSA, &snap)

	want := SavingsSnapshot{
		TotalSizeKiB:    1024 + 512,
		TotalRssKiB:     800 + 400,
		TotalPssKiB:     50 + 25,
		SharedCleanKiB:  780 + 390,
		SharedDirtyKiB:  0,
		PrivateCleanKiB: 20 + 10,
		PrivateDirtyKiB: 0,
		JVMs:            1, // single accumulate call → 1 process
	}
	if snap != want {
		t.Errorf("snapshot mismatch\n got %+v\nwant %+v", snap, want)
	}
}

func TestAccumulateSmaps_NoJSA(t *testing.T) {
	const noJSA = `7f9999000000-7f9999300000 r-xp 00000000 fd:00 99999  /usr/lib/libc.so.6
Size:               2000 kB
Rss:                1500 kB
VmFlags: rd ex mr mw me
`
	snap := SavingsSnapshot{}
	accumulateSmaps(noJSA, &snap)
	want := SavingsSnapshot{}
	if snap != want {
		t.Errorf("non-jsa smaps must not contribute\n got %+v\nwant %+v", snap, want)
	}
}

func TestAccumulateSmaps_TwoProcesses(t *testing.T) {
	// Two invocations should add up to JVMs=2.
	snap := SavingsSnapshot{}
	accumulateSmaps(smapsFixtureJSA, &snap)
	accumulateSmaps(smapsFixtureJSA, &snap)
	if snap.JVMs != 2 {
		t.Errorf("two invocations → JVMs=2; got %d", snap.JVMs)
	}
	if snap.TotalRssKiB != (800+400)*2 {
		t.Errorf("Rss should double; got %d", snap.TotalRssKiB)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  Diagnose
// ─────────────────────────────────────────────────────────────────────────────

func TestDiagnose_AllOK(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "config current-context", "kind-mc-1\n", nil).
		on("valkey-cli", "", "PONG\n", nil)
	f.install()

	d := (&App{}).Diagnose("127.0.0.1", 6379)
	if !d.KubectlOK || d.KubectlContext != "kind-mc-1" {
		t.Errorf("kubectl: %+v", d)
	}
	if !d.ValkeyReachable {
		t.Errorf("valkey should be reachable: %+v", d)
	}
	if d.Note != "" {
		t.Errorf("note should be empty on full success: %q", d.Note)
	}
	if d.ValkeyAddr != "127.0.0.1:6379" {
		t.Errorf("addr formatting: %q", d.ValkeyAddr)
	}
}

func TestDiagnose_KubectlDown(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "", "", errors.New("exec: \"kubectl\": not found")).
		on("valkey-cli", "", "PONG\n", nil)
	f.install()

	d := (&App{}).Diagnose("h", 1)
	if d.KubectlOK {
		t.Error("kubectl should be down")
	}
	if !d.ValkeyReachable {
		t.Error("valkey should still be reachable")
	}
	if !strings.Contains(d.Note, "kubectl not configured") {
		t.Errorf("note should mention kubectl: %q", d.Note)
	}
}

func TestDiagnose_ValkeyCliMissing_RedisCliRescues(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "", "ctx\n", nil).
		on("valkey-cli", "", "", errors.New("valkey-cli: not found")).
		on("redis-cli", "", "PONG\n", nil)
	f.install()

	d := (&App{}).Diagnose("h", 1)
	if !d.ValkeyReachable {
		t.Error("redis-cli fallback should have succeeded")
	}
	if d.Note != "" {
		t.Errorf("no note when fallback succeeds: %q", d.Note)
	}
}

func TestDiagnose_BothValkeyDown(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "", "ctx\n", nil).
		on("valkey-cli", "", "", errors.New("conn refused")).
		on("redis-cli", "", "", errors.New("conn refused"))
	f.install()

	d := (&App{}).Diagnose("h", 1)
	if d.ValkeyReachable {
		t.Error("valkey should be unreachable")
	}
	if !strings.Contains(d.Note, "valkey unreachable") {
		t.Errorf("note should mention valkey failure: %q", d.Note)
	}
}

func TestDiagnose_BothDown_NoteContainsBoth(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "", "", errors.New("not found")).
		on("valkey-cli", "", "", errors.New("nope")).
		on("redis-cli", "", "", errors.New("nope"))
	f.install()

	d := (&App{}).Diagnose("h", 1)
	if !strings.Contains(d.Note, "kubectl") || !strings.Contains(d.Note, "valkey") {
		t.Errorf("note should mention both failures: %q", d.Note)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  ListClassCaches
// ─────────────────────────────────────────────────────────────────────────────

const classCachesJSON = `{
  "items": [
    {
      "metadata": { "name": "svc-a", "namespace": "demo" },
      "spec": {
        "profile": "default",
        "workloadRef": { "name": "svc-a-deploy" }
      },
      "status": { "phase": "Ready", "archiveKey": "deadbeef00112233" }
    },
    {
      "metadata": { "name": "svc-b", "namespace": "shop" },
      "spec": {
        "profile": "low-mem",
        "workloadRef": { "name": "svc-b-deploy" }
      },
      "status": { "phase": "Pending", "archiveKey": "" }
    }
  ]
}`

func TestListClassCaches_ProjectsAllFields(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "get classcaches.classcache.dev", classCachesJSON, nil)
	f.install()

	out, err := (&App{}).ListClassCaches()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len=%d, want 2", len(out))
	}
	want0 := ClassCacheSummary{Name: "svc-a", Namespace: "demo", WorkloadName: "svc-a-deploy", Profile: "default", Phase: "Ready", ArchiveKey: "deadbeef00112233"}
	if out[0] != want0 {
		t.Errorf("[0]\n got %+v\nwant %+v", out[0], want0)
	}
	want1 := ClassCacheSummary{Name: "svc-b", Namespace: "shop", WorkloadName: "svc-b-deploy", Profile: "low-mem", Phase: "Pending", ArchiveKey: ""}
	if out[1] != want1 {
		t.Errorf("[1]\n got %+v\nwant %+v", out[1], want1)
	}
}

func TestListClassCaches_Empty(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "get classcaches.classcache.dev", `{"items":[]}`, nil)
	f.install()

	out, err := (&App{}).ListClassCaches()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestListClassCaches_KubectlError(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "", "", errors.New("not found"))
	f.install()

	if _, err := (&App{}).ListClassCaches(); err == nil {
		t.Error("expected kubectl error to surface")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  ListArchives
// ─────────────────────────────────────────────────────────────────────────────

func TestListArchives_HappyPath(t *testing.T) {
	// KEYS returns a mix of canonical archive keys, peer set keys, and a junk
	// key. Only the two 16-hex archive keys should make it through.
	const keys = `archive:e3b0c44298fc1c14
archive:e3b0c44298fc1c14:peers
archive:e3b0c44298fc1c14:build_lock
archive:7c5d2afebf4729a1
archive:short
archive:e3b0c44298fc1c14:peer-zone:zone-a
`
	const hgetallA = `size
1024
jvm
OpenJDK 22.0.1
arch
arm64
`
	const hgetallB = `size
2048
jvm
OpenJDK 21.0.4
arch
amd64
`
	const smembersA = `10.244.1.5:7777
10.244.1.8:7777
`
	const smembersB = `10.244.2.3:7777
`

	f := newFakeRun(t).
		on("valkey-cli", "-h h -p 1 --raw KEYS archive:*", keys, nil).
		on("valkey-cli", "-h h -p 1 --raw HGETALL archive:e3b0c44298fc1c14", hgetallA, nil).
		on("valkey-cli", "-h h -p 1 --raw HGETALL archive:7c5d2afebf4729a1", hgetallB, nil).
		on("valkey-cli", "-h h -p 1 --raw SMEMBERS archive:e3b0c44298fc1c14:peers", smembersA, nil).
		on("valkey-cli", "-h h -p 1 --raw SMEMBERS archive:7c5d2afebf4729a1:peers", smembersB, nil)
	f.install()

	got, err := (&App{}).ListArchives("h", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2 archives only (peers/lock/junk filtered)", len(got))
	}

	byKey := map[string]ArchiveSummary{}
	for _, a := range got {
		byKey[a.Key] = a
	}
	a, ok := byKey["e3b0c44298fc1c14"]
	if !ok {
		t.Fatal("missing archive A")
	}
	if a.SizeBytes != 1024 || a.JVM != "OpenJDK 22.0.1" || a.Arch != "arm64" {
		t.Errorf("A metadata: %+v", a)
	}
	if len(a.PeerEndpoints) != 2 || a.PeerEndpoints[0] != "10.244.1.5:7777" {
		t.Errorf("A peers: %v", a.PeerEndpoints)
	}

	b, ok := byKey["7c5d2afebf4729a1"]
	if !ok {
		t.Fatal("missing archive B")
	}
	if b.SizeBytes != 2048 || b.Arch != "amd64" {
		t.Errorf("B metadata: %+v", b)
	}
}

func TestListArchives_FallsBackToRedisCli(t *testing.T) {
	// valkey-cli missing — every call should retry on redis-cli.
	notFound := errors.New("valkey-cli: not found")
	const keys = "archive:e3b0c44298fc1c14\n"
	const hgetall = "size\n100\njvm\nx\narch\ny\n"
	const smembers = "p1:1\n"

	f := newFakeRun(t).
		on("valkey-cli", "", "", notFound).
		on("redis-cli", "-h h -p 1 --raw KEYS archive:*", keys, nil).
		on("redis-cli", "-h h -p 1 --raw HGETALL archive:e3b0c44298fc1c14", hgetall, nil).
		on("redis-cli", "-h h -p 1 --raw SMEMBERS archive:e3b0c44298fc1c14:peers", smembers, nil)
	f.install()

	got, err := (&App{}).ListArchives("h", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SizeBytes != 100 || got[0].JVM != "x" {
		t.Errorf("redis-cli fallback failed: %+v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  SampleSavings
// ─────────────────────────────────────────────────────────────────────────────

func TestSampleSavings_KindFastPath(t *testing.T) {
	// Two pods on node-a, one on node-b. docker exec works on both.
	podList := "demo|svc-a-0|node-a\ndemo|svc-a-1|node-a\nshop|svc-b-0|node-b\n"

	f := newFakeRun(t).
		on("kubectl", "get pod -A", podList, nil).
		on("docker", "exec node-a pgrep", "100\n200\n", nil).
		on("docker", "exec node-a cat /proc/100/smaps", smapsFixtureJSA, nil).
		on("docker", "exec node-a cat /proc/200/smaps", smapsFixtureJSA, nil).
		on("docker", "exec node-b pgrep", "300\n", nil).
		on("docker", "exec node-b cat /proc/300/smaps", smapsFixtureJSA, nil)
	f.install()

	snap, err := (&App{}).SampleSavings()
	if err != nil {
		t.Fatal(err)
	}
	if snap.JVMs != 3 {
		t.Errorf("JVMs=%d, want 3", snap.JVMs)
	}
	wantRss := uint64((800 + 400) * 3)
	if snap.TotalRssKiB != wantRss {
		t.Errorf("Rss=%d, want %d", snap.TotalRssKiB, wantRss)
	}
	// SavedKiB = Rss - Pss
	wantSaved := wantRss - uint64((50+25)*3)
	if snap.SavedKiB != wantSaved {
		t.Errorf("Saved=%d, want %d", snap.SavedKiB, wantSaved)
	}
}

func TestSampleSavings_KubectlExecFallback(t *testing.T) {
	// node-a: docker exec works (1 PID). node-b: docker exec fails → fallback
	// to kubectl exec for each pod scheduled on node-b.
	podList := "demo|svc-a-0|node-a\nshop|svc-b-0|node-b\nshop|svc-b-1|node-b\n"

	f := newFakeRun(t).
		on("kubectl", "get pod -A", podList, nil).
		on("docker", "exec node-a pgrep", "100\n", nil).
		on("docker", "exec node-a cat /proc/100/smaps", smapsFixtureJSA, nil).
		on("docker", "exec node-b pgrep", "", errors.New("no such container")).
		on("kubectl", "exec -n shop svc-b-0 -- cat /proc/1/smaps", smapsFixtureJSA, nil).
		on("kubectl", "exec -n shop svc-b-1 -- cat /proc/1/smaps", smapsFixtureJSA, nil)
	f.install()

	snap, err := (&App{}).SampleSavings()
	if err != nil {
		t.Fatal(err)
	}
	if snap.JVMs != 3 {
		t.Errorf("JVMs=%d, want 3 (1 kind + 2 fallback)", snap.JVMs)
	}
}

func TestSampleSavings_NoPods(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "get pod -A", "", nil)
	f.install()

	snap, err := (&App{}).SampleSavings()
	if err != nil {
		t.Fatal(err)
	}
	if snap.JVMs != 0 || snap.TotalRssKiB != 0 {
		t.Errorf("empty pod list should produce zeroed snapshot, got %+v", snap)
	}
}

func TestSampleSavings_KubectlListError(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "get pod -A", "", errors.New("conn refused"))
	f.install()

	if _, err := (&App{}).SampleSavings(); err == nil {
		t.Error("kubectl list error should surface")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  PodStats
// ─────────────────────────────────────────────────────────────────────────────

func TestPodStats_HappyPath(t *testing.T) {
	podList := "demo|svc-a-0|node-a\ndemo|svc-a-1|node-a\nshop|svc-b-0|node-b\n"
	topDemo := "svc-a-0   100m   256Mi\nsvc-a-1   200m   512Mi\n"
	topShop := "svc-b-0   1500m  1Gi\n"

	f := newFakeRun(t).
		on("kubectl", "get pod -A", podList, nil).
		on("kubectl", "top pod -n demo", topDemo, nil).
		on("kubectl", "top pod -n shop", topShop, nil)
	f.install()

	got, err := (&App{}).PodStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d, want 3", len(got))
	}
	byName := map[string]PodStat{}
	for _, p := range got {
		byName[p.Name] = p
	}
	cases := []struct {
		name   string
		cpu    int
		mem    int
		ns     string
		node   string
	}{
		{"svc-a-0", 100, 256, "demo", "node-a"},
		{"svc-a-1", 200, 512, "demo", "node-a"},
		{"svc-b-0", 1500, 1024, "shop", "node-b"},
	}
	for _, c := range cases {
		p := byName[c.name]
		if p.CPUMilli != c.cpu || p.MemMiB != c.mem || p.Namespace != c.ns || p.Node != c.node {
			t.Errorf("%s: got %+v, want cpu=%d mem=%d ns=%s node=%s",
				c.name, p, c.cpu, c.mem, c.ns, c.node)
		}
	}
}

func TestPodStats_MetricsServerDown(t *testing.T) {
	// kubectl top fails for every namespace — pods still listed, metrics zeroed.
	podList := "demo|svc-a-0|node-a\n"
	f := newFakeRun(t).
		on("kubectl", "get pod -A", podList, nil).
		on("kubectl", "top pod", "", errors.New("metrics not available"))
	f.install()

	got, err := (&App{}).PodStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	if got[0].CPUMilli != 0 || got[0].MemMiB != 0 {
		t.Errorf("metrics should be zeroed: %+v", got[0])
	}
	if got[0].Name != "svc-a-0" || got[0].Node != "node-a" {
		t.Errorf("identity should still be present: %+v", got[0])
	}
}

func TestPodStats_EmptyList(t *testing.T) {
	f := newFakeRun(t).
		on("kubectl", "get pod -A", "", nil)
	f.install()

	got, err := (&App{}).PodStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
//  Plumbing sanity
// ─────────────────────────────────────────────────────────────────────────────

func TestFakeRun_MatchesAndLogs(t *testing.T) {
	f := newFakeRun(t).on("foo", "bar", "ok", nil)
	f.install()
	out, err := run("foo", "bar")
	if err != nil || out != "ok" {
		t.Errorf("matcher should fire: out=%q err=%v", out, err)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "foo bar") {
		t.Errorf("call log: %v", f.calls)
	}
}
