package com.example.apm.runtime;

import com.example.apm.runtime.exporter.Exporter;
import com.example.apm.runtime.exporter.StdoutJsonExporter;

import java.util.concurrent.ThreadLocalRandom;

public final class Tracer {
    private static volatile Exporter EXPORTER = new StdoutJsonExporter();

    private Tracer() {}

    public static Span startSpan(String name) {
        Span parent = SpanContext.current();
        String traceId = (parent != null) ? parent.traceId : newId128();
        String parentId = (parent != null) ? parent.spanId : "";
        String spanId = newId64();
        Span s = new Span(traceId, spanId, parentId, name,
            System.nanoTime(), System.currentTimeMillis());
        SpanContext.push(s);
        return s;
    }

    public static Span startHttpSpan(Object req) {
        String method = null;
        String uri = null;
        try {
            method = (String) req.getClass().getMethod("getMethod").invoke(req);
            Object u = req.getClass().getMethod("getRequestURI").invoke(req);
            if (u != null) uri = u.toString();
        } catch (Throwable ignore) {
        }
        String name = (method != null && uri != null) ? (method + " " + uri) : "http.request";
        Span s = startSpan(name);
        if (method != null) s.setAttr("http.method", method);
        if (uri != null)    s.setAttr("http.path", uri);
        return s;
    }

    public static void endHttpSpan(Span span, Throwable t) {
        if (span == null) return;
        if (t != null) {
            span.setAttr("error", t.getClass().getName());
            String msg = t.getMessage();
            if (msg != null) span.setAttr("error.msg", msg);
        }
        span.end();
    }

    public static Exporter exporter() {
        return EXPORTER;
    }

    public static void setExporter(Exporter e) {
        if (e != null) EXPORTER = e;
    }

    private static String newId64() {
        return Long.toHexString(ThreadLocalRandom.current().nextLong());
    }

    private static String newId128() {
        return Long.toHexString(ThreadLocalRandom.current().nextLong())
             + Long.toHexString(ThreadLocalRandom.current().nextLong());
    }
}
