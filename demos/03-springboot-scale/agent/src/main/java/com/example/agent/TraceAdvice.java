package com.example.agent;

import net.bytebuddy.asm.Advice;

public class TraceAdvice {
    @Advice.OnMethodEnter
    public static void enter(@Advice.Origin("#t.#m") String fqMethod) {
        System.out.println("[TRACE-ENTER] " + fqMethod);
    }

    @Advice.OnMethodExit
    public static void exit(@Advice.Origin("#t.#m") String fqMethod) {
        System.out.println("[TRACE-EXIT] " + fqMethod);
    }
}
