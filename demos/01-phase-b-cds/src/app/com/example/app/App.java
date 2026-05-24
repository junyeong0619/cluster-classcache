package com.example.app;

public class App {
    public static void main(String[] args) throws Exception {
        System.out.println("[APP] start");
        for (int i = 0; i < 3; i++) {
            doWork(i);
        }
        System.out.println("[APP] done");
    }

    public static void doWork(int n) {
        System.out.println("[APP] doWork " + n);
    }
}
