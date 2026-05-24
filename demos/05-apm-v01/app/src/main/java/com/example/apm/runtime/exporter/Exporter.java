package com.example.apm.runtime.exporter;

import com.example.apm.runtime.Span;

public interface Exporter {
    void export(Span span);
}
