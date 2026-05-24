package com.example.apm.runtime;

import java.util.ArrayDeque;
import java.util.Deque;

public final class SpanContext {
    private static final ThreadLocal<Deque<Span>> STACK =
        ThreadLocal.withInitial(ArrayDeque::new);

    private SpanContext() {}

    public static Span current() {
        return STACK.get().peek();
    }

    static void push(Span s) {
        STACK.get().push(s);
    }

    static void pop(Span s) {
        Deque<Span> stack = STACK.get();
        if (!stack.isEmpty() && stack.peek() == s) {
            stack.pop();
        }
        if (stack.isEmpty()) STACK.remove();
    }
}
