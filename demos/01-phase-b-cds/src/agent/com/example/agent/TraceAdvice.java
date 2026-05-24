package com.example.agent;

import net.bytebuddy.asm.Advice;

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
