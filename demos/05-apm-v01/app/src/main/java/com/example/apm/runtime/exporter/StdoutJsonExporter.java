package com.example.apm.runtime.exporter;

import com.example.apm.runtime.Span;

import java.util.Map;

public final class StdoutJsonExporter implements Exporter {

    @Override
    public void export(Span span) {
        long durNs = span.endNanos - span.startNanos;
        StringBuilder sb = new StringBuilder(256);
        sb.append("[SPAN] {")
          .append("\"trace\":\"").append(span.traceId).append('"')
          .append(",\"span\":\"").append(span.spanId).append('"')
          .append(",\"parent\":\"").append(span.parentId).append('"')
          .append(",\"name\":\"").append(escape(span.name)).append('"')
          .append(",\"start_ms\":").append(span.startWallMs)
          .append(",\"dur_us\":").append(durNs / 1000L)
          .append(",\"attrs\":{");
        boolean first = true;
        for (Map.Entry<String, String> e : span.attrs.entrySet()) {
            if (!first) sb.append(',');
            first = false;
            sb.append('"').append(escape(e.getKey())).append("\":\"")
              .append(escape(e.getValue())).append('"');
        }
        sb.append("}}");
        System.out.println(sb);
    }

    private static String escape(String s) {
        if (s == null) return "";
        StringBuilder out = null;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            String repl = null;
            if (c == '\\') repl = "\\\\";
            else if (c == '"') repl = "\\\"";
            else if (c == '\n') repl = "\\n";
            else if (c == '\r') repl = "\\r";
            else if (c == '\t') repl = "\\t";
            else if (c < 0x20) repl = String.format("\\u%04x", (int) c);
            if (repl != null) {
                if (out == null) out = new StringBuilder(s.length() + 8).append(s, 0, i);
                out.append(repl);
            } else if (out != null) {
                out.append(c);
            }
        }
        return out == null ? s : out.toString();
    }
}
