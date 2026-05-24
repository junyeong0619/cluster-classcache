package com.example.apm.agent;

import com.example.apm.runtime.Span;
import com.example.apm.runtime.Tracer;
import net.bytebuddy.asm.Advice;

public class HttpEntryAdvice {

    @Advice.OnMethodEnter
    public static Span enter(@Advice.Argument(0) Object req) {
        return Tracer.startHttpSpan(req);
    }

    @Advice.OnMethodExit(onThrowable = Throwable.class)
    public static void exit(@Advice.Enter Span span,
                            @Advice.Thrown Throwable thrown) {
        Tracer.endHttpSpan(span, thrown);
    }
}
