package com.example.apm.web;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RestController;

@SpringBootApplication
@RestController
public class App {
    public static void main(String[] args) {
        SpringApplication.run(App.class, args);
    }

    @GetMapping("/hello")
    public String hello() {
        return "hello pid=" + ProcessHandle.current().pid();
    }

    @GetMapping("/work/{n}")
    public String work(@PathVariable int n) {
        long sum = 0;
        for (int i = 0; i < n; i++) sum += i;
        return "result=" + sum;
    }
}
