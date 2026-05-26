package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestPeerServerServesArchive(t *testing.T) {
	dir := t.TempDir()
	content := []byte("archive-bytes-xyz")
	if err := os.WriteFile(ArchivePath(dir, "abc123"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	srv := &PeerServer{ArchiveDir: dir, Port: port}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	time.Sleep(100 * time.Millisecond)

	dest := filepath.Join(t.TempDir(), "pulled.jsa")
	n, err := PullFromPeer(context.Background(),
		fmt.Sprintf("127.0.0.1:%d", port), "abc123", dest, 2*time.Second, "")
	if err != nil {
		t.Fatalf("PullFromPeer: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("pulled %d bytes, want %d", n, len(content))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch")
	}
}

func TestPullFromPeer_SHA256Mismatch(t *testing.T) {
	dir := t.TempDir()
	content := []byte("v0.11 verifies this")
	if err := os.WriteFile(ArchivePath(dir, "abc123"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	srv := &PeerServer{ArchiveDir: dir, Port: port}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	time.Sleep(100 * time.Millisecond)

	dest := filepath.Join(t.TempDir(), "bad.jsa")
	wrong := strings.Repeat("0", 64) // a clearly wrong sha256
	_, err := PullFromPeer(context.Background(),
		fmt.Sprintf("127.0.0.1:%d", port), "abc123", dest, 2*time.Second, wrong)
	if err == nil {
		t.Fatal("expected sha256 mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("want mismatch error, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("mismatched download must be removed")
	}
}

func TestPeerServerMissingArchive(t *testing.T) {
	port := freePort(t)
	srv := &PeerServer{ArchiveDir: t.TempDir(), Port: port}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	time.Sleep(100 * time.Millisecond)

	dest := filepath.Join(t.TempDir(), "wont-exist.jsa")
	if _, err := PullFromPeer(context.Background(),
		fmt.Sprintf("127.0.0.1:%d", port), "nope", dest, 2*time.Second, ""); err == nil {
		t.Errorf("expected pull error for missing archive")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("partial file leftover after failed pull")
	}
}
