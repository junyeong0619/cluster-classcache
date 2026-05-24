package com.example.apm.agent;

import net.bytebuddy.agent.builder.AgentBuilder;
import net.bytebuddy.asm.Advice;
import net.bytebuddy.matcher.ElementMatchers;

import java.lang.instrument.Instrumentation;

public class ApmAgent {
    public static void premain(String args, Instrumentation inst) {
        System.out.println("[APM-AGENT] premain");
        new AgentBuilder.Default()
            .disableClassFormatChanges()
            .with(AgentBuilder.InitializationStrategy.NoOp.INSTANCE)
            .with(AgentBuilder.TypeStrategy.Default.REDEFINE)
            .with(AgentBuilder.RedefinitionStrategy.DISABLED)
            .type(ElementMatchers.named("org.springframework.web.servlet.DispatcherServlet"))
            .transform((builder, type, classLoader, module, pd) ->
                builder.visit(Advice.to(HttpEntryAdvice.class)
                    .on(ElementMatchers.named("doDispatch")))
            )
            .installOn(inst);
        System.out.println("[APM-AGENT] installed (DispatcherServlet.doDispatch)");
    }
}
