package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestDirectory(t *testing.T) (*Directory, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	cli := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = cli.Close() })
	return newDirectoryWithClient(cli), mr
}

func TestDirectoryWaitReady(t *testing.T) {
	d, _ := newTestDirectory(t)
	if err := d.WaitReady(context.Background(), 2*time.Second); err != nil {
		t.Errorf("WaitReady: %v", err)
	}
}

func TestDirectoryRegisterAndListPeers(t *testing.T) {
	d, _ := newTestDirectory(t)
	ctx := context.Background()

	if err := d.Register(ctx, "k1", "host-a:8088", 1024, "jdk-22", "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(ctx, "k1", "host-b:8088", 1024, "jdk-22", "amd64"); err != nil {
		t.Fatal(err)
	}

	peers, err := d.ListPeers(ctx, "k1", "host-a:8088")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0] != "host-b:8088" {
		t.Errorf("ListPeers excluding self = %v", peers)
	}
}

func TestDirectoryBuildLock(t *testing.T) {
	d, _ := newTestDirectory(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		holder string
		want   bool
	}{
		{"first wins", "host-a:8088", true},
		{"second loses", "host-b:8088", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.TryAcquireBuildLock(ctx, "k-lock", tc.holder, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("TryAcquireBuildLock(%s) = %v, want %v", tc.holder, got, tc.want)
			}
		})
	}
}

func TestDirectoryPublishEvent(t *testing.T) {
	d, mr := newTestDirectory(t)
	ctx := context.Background()
	if err := d.PublishEvent(ctx, PrimerEvent{
		Node: "node-a", Key: "k1", Method: "built-locally",
		ElapsedMS: 1234, ArchiveSize: 4096,
	}); err != nil {
		t.Fatal(err)
	}
	// miniredis doesn't expose subscriber counts directly but Publish should
	// not error and the call path must work end-to-end.
	if mr.PubSubNumSub("primer-events")["primer-events"] != 0 {
		// no assertion — just exercise the API
	}
}

func TestDirectoryUnregister(t *testing.T) {
	d, _ := newTestDirectory(t)
	ctx := context.Background()

	_ = d.Register(ctx, "k1", "host-a:8088", 1024, "jdk-22", "amd64")
	_ = d.Register(ctx, "k1", "host-b:8088", 1024, "jdk-22", "amd64")
	if err := d.Unregister(ctx, "k1", "host-a:8088"); err != nil {
		t.Fatal(err)
	}
	peers, _ := d.ListPeers(ctx, "k1", "")
	for _, p := range peers {
		if p == "host-a:8088" {
			t.Errorf("host-a:8088 should have been removed; got peers=%v", peers)
		}
	}
}

func TestDirectoryRenewBuildLock(t *testing.T) {
	d, _ := newTestDirectory(t)
	ctx := context.Background()

	got, _ := d.TryAcquireBuildLock(ctx, "k", "holder-a", time.Minute)
	if !got {
		t.Fatal("first acquire should win")
	}
	// Same holder can renew.
	ok, err := d.RenewBuildLock(ctx, "k", "holder-a", 2*time.Minute)
	if err != nil || !ok {
		t.Errorf("renew by holder should succeed; ok=%v err=%v", ok, err)
	}
	// Different holder cannot renew.
	ok, err = d.RenewBuildLock(ctx, "k", "imposter", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("renew by non-holder must fail")
	}
}

func TestDirectoryReleaseBuildLock(t *testing.T) {
	d, _ := newTestDirectory(t)
	ctx := context.Background()

	_, _ = d.TryAcquireBuildLock(ctx, "k", "holder-a", time.Minute)
	// Non-holder release is a no-op.
	if err := d.ReleaseBuildLock(ctx, "k", "imposter"); err != nil {
		t.Fatal(err)
	}
	got, _ := d.TryAcquireBuildLock(ctx, "k", "holder-b", time.Minute)
	if got {
		t.Error("lock should still be held by holder-a after imposter release")
	}
	// Holder release actually deletes.
	if err := d.ReleaseBuildLock(ctx, "k", "holder-a"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.TryAcquireBuildLock(ctx, "k", "holder-b", time.Minute)
	if !got {
		t.Error("after legitimate release, the next acquire should win")
	}
}

func TestFormatEndpoint(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"node-a", 8088, "node-a:8088"},
		{"10.0.0.1", 6379, "10.0.0.1:6379"},
	}
	for _, tc := range cases {
		if got := FormatEndpoint(tc.host, tc.port); got != tc.want {
			t.Errorf("FormatEndpoint(%s,%d) = %s", tc.host, tc.port, got)
		}
	}
}
