package com.example.apm.runtime;

import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

public final class Span {
    public final String traceId;
    public final String spanId;
    public final String parentId;
    public final String name;
    public final long startNanos;
    public final long startWallMs;
    public volatile long endNanos;
    public final Map<String, String> attrs = new ConcurrentHashMap<>();

    Span(String traceId, String spanId, String parentId, String name,
         long startNanos, long startWallMs) {
        this.traceId = traceId;
        this.spanId = spanId;
        this.parentId = parentId;
        this.name = name;
        this.startNanos = startNanos;
        this.startWallMs = startWallMs;
    }

    public void setAttr(String k, String v) {
        if (k != null && v != null) attrs.put(k, v);
    }

    public void end() {
        this.endNanos = System.nanoTime();
        SpanContext.pop(this);
        Tracer.exporter().export(this);
    }
}
