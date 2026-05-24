package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"
)

type Config struct {
	NodeName    string
	PeerHost    string
	PeerPort    int
	ArchiveDir  string
	AppJar      string
	AgentJar    string
	ExtractDir  string
	ProfilePath string

	BuildLockTTL  time.Duration
	PeerPullTimeout time.Duration
	WaitForPeerTimeout time.Duration
}

type Orchestrator struct {
	cfg       Config
	profile   *Profile
	dir       *Directory
	publisher *StatusPublisher // nil = not running under operator
}

func NewOrchestrator(cfg Config, profile *Profile, dir *Directory, pub *StatusPublisher) *Orchestrator {
	return &Orchestrator{cfg: cfg, profile: profile, dir: dir, publisher: pub}
}

func (o *Orchestrator) logf(format string, args ...any) {
	log.Printf("[primer/%s] "+format, append([]any{o.cfg.NodeName}, args...)...)
}

// Run executes the full lifecycle once and then starts the peer server.
// Returns the chosen method and elapsed time so main can publish events.
func (o *Orchestrator) Run(ctx context.Context, peer *PeerServer) (method string, elapsed time.Duration, archiveSize int64, err error) {
	o.logf("starting (profile=%s)", o.profile.Metadata.Name)

	if err := o.dir.WaitReady(ctx, 30*time.Second); err != nil {
		return "", 0, 0, err
	}
	o.logf("valkey ok")

	key, err := ComputeKey(o.cfg.AppJar, o.cfg.AgentJar, o.profile.Metadata.Name)
	if err != nil {
		return "", 0, 0, fmt.Errorf("compute key: %w", err)
	}
	o.logf("archive key = %s", key)

	t0 := time.Now()
	selfEP := FormatEndpoint(o.cfg.PeerHost, o.cfg.PeerPort)
	archivePath := ArchivePath(o.cfg.ArchiveDir, key)

	switch {
	case LocalArchiveExists(o.cfg.ArchiveDir, key):
		method = "local-hit"
	default:
		method, err = o.acquireRemoteOrBuild(ctx, key, selfEP)
		if err != nil {
			return "", 0, 0, err
		}
	}

	st, err := os.Stat(archivePath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("archive missing after method=%s: %w", method, err)
	}
	archiveSize = st.Size()

	jvm, _ := jvmVersion()
	if err := o.dir.Register(ctx, key, selfEP, archiveSize, jvm, runtime.GOARCH); err != nil {
		return "", 0, 0, fmt.Errorf("register: %w", err)
	}
	o.logf("registered: peer=%s key=%s", selfEP, key)

	elapsed = time.Since(t0)
	o.logf("READY method=%s elapsed_ms=%d archive_size=%d", method, elapsed.Milliseconds(), archiveSize)

	if err := o.dir.PublishEvent(ctx, PrimerEvent{
		Node: o.cfg.NodeName, Key: key, Method: method,
		ElapsedMS: elapsed.Milliseconds(), ArchiveSize: archiveSize,
	}); err != nil {
		o.logf("publish event warning: %v", err)
	}

	if err := o.publisher.PublishArchiveKey(ctx, key, archiveSize); err != nil {
		o.logf("publish status warning: %v", err)
	} else if o.publisher != nil {
		o.logf("status published to ClassCache CR")
	}

	if err := peer.Start(); err != nil {
		return "", 0, 0, fmt.Errorf("start peer server: %w", err)
	}
	o.logf("peer-srv listening on 0.0.0.0:%d", o.cfg.PeerPort)
	return method, elapsed, archiveSize, nil
}

func (o *Orchestrator) acquireRemoteOrBuild(ctx context.Context, key, selfEP string) (string, error) {
	peers, err := o.dir.ListPeers(ctx, key, selfEP)
	if err != nil {
		return "", err
	}
	o.logf("directory has %d peer(s): %v", len(peers), peers)
	dest := ArchivePath(o.cfg.ArchiveDir, key)
	for _, peer := range peers {
		o.logf("  trying pull: http://%s/archive/%s", peer, key)
		n, err := PullFromPeer(ctx, peer, key, dest, o.cfg.PeerPullTimeout)
		if err == nil {
			o.logf("  pulled %d bytes from %s", n, peer)
			return "pulled-from:" + peer, nil
		}
		o.logf("  pull failed: %v", err)
	}

	got, err := o.dir.TryAcquireBuildLock(ctx, key, selfEP, o.cfg.BuildLockTTL)
	if err != nil {
		return "", fmt.Errorf("build lock: %w", err)
	}
	if got {
		o.logf("acquired build lock — building locally")
		if err := BuildLocally(ctx, o.profile, o.cfg.AppJar, o.cfg.ExtractDir, dest); err != nil {
			return "", fmt.Errorf("build: %w", err)
		}
		return "built-locally", nil
	}
	return o.waitForPeer(ctx, key, selfEP, dest)
}

func (o *Orchestrator) waitForPeer(ctx context.Context, key, selfEP, dest string) (string, error) {
	o.logf("  another node is building — polling for peer...")
	deadline := time.Now().Add(o.cfg.WaitForPeerTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		peers, err := o.dir.ListPeers(ctx, key, selfEP)
		if err != nil {
			continue
		}
		for _, peer := range peers {
			n, err := PullFromPeer(ctx, peer, key, dest, o.cfg.PeerPullTimeout)
			if err == nil {
				o.logf("  pulled-after-wait %d bytes from %s", n, peer)
				return "pulled-after-wait:" + peer, nil
			}
		}
	}
	return "", fmt.Errorf("timeout waiting for peer to publish key=%s", key)
}
