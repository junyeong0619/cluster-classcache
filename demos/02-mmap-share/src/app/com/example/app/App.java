package com.example.app;

import java.util.HashMap;
import java.util.ArrayList;
import java.util.regex.Pattern;
import java.util.stream.IntStream;

public class App {
    public static void main(String[] args) throws Exception {
        boolean loop = args.length > 0 && "--loop".equals(args[0]);
        long pid = ProcessHandle.current().pid();
        System.out.println("[APP] start pid=" + pid + " loop=" + loop);

        warmupClasses();

        for (int i = 0; i < 3; i++) {
            doWork(i);
        }

        if (loop) {
            System.out.println("[APP] entering idle loop pid=" + pid);
            while (true) {
                Thread.sleep(60_000);
            }
        }

        System.out.println("[APP] done pid=" + pid);
    }

    public static void doWork(int n) {
        System.out.println("[APP] doWork " + n + " pid=" + ProcessHandle.current().pid());
    }

    private static void warmupClasses() {
        HashMap<String, Integer> m = new HashMap<>();
        for (int i = 0; i < 20; i++) m.put("k" + i, i);
        ArrayList<String> list = new ArrayList<>(m.keySet());
        Pattern.compile("[a-z]+\\d+").matcher("k5").matches();
        int sum = IntStream.range(0, 100).sum();
        if (sum < 0 || list.isEmpty()) throw new IllegalStateException();
    }
}
