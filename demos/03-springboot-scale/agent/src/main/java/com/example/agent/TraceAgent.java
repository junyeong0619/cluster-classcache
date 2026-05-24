package com.example.agent;

import net.bytebuddy.agent.builder.AgentBuilder;
import net.bytebuddy.asm.Advice;
import net.bytebuddy.matcher.ElementMatchers;

import java.lang.instrument.Instrumentation;

public class TraceAgent {
    public static void premain(String args, Instrumentation inst) {
        System.out.println("[AGENT] premain");
        new AgentBuilder.Default()
            .disableClassFormatChanges()
            .with(AgentBuilder.InitializationStrategy.NoOp.INSTANCE)
            .with(AgentBuilder.TypeStrategy.Default.REDEFINE)
            .with(AgentBuilder.RedefinitionStrategy.DISABLED)
            .type(ElementMatchers.named("com.example.web.App"))
            .transform((builder, type, classLoader, module, pd) ->
                builder.visit(Advice.to(TraceAdvice.class)
                    .on(ElementMatchers.namedOneOf("hello", "work")))
            )
            .installOn(inst);
        System.out.println("[AGENT] installed");
    }
}
