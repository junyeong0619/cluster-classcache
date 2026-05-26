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
	PeerZone    string // optional — e.g. "us-east-1a"; "" = zone-aware off
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

	// Set after Run() succeeds, used by GracefulShutdown to unregister.
	registeredKey      string
	registeredEndpoint string
}

func NewOrchestrator(cfg Config, profile *Profile, dir *Directory, pub *StatusPublisher) *Orchestrator {
	return &Orchestrator{cfg: cfg, profile: profile, dir: dir, publisher: pub}
}

// GracefulShutdown removes our endpoint from the directory peer set so that
// next-time peer lookups don't include a dead PodIP. Call on SIGTERM.
// Safe to call when Run() never reached the register step (no-op).
func (o *Orchestrator) GracefulShutdown(ctx context.Context) {
	if o.registeredKey == "" || o.registeredEndpoint == "" {
		return
	}
	if err := o.dir.Unregister(ctx, o.registeredKey, o.registeredEndpoint); err != nil {
		o.logf("graceful unregister failed: %v", err)
		return
	}
	o.logf("graceful unregister: removed %s from archive:%s:peers",
		o.registeredEndpoint, o.registeredKey)
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
	archiveSHA, err := sha256File(archivePath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("hash archive: %w", err)
	}
	if err := o.dir.Register(ctx, key, selfEP, archiveSize, jvm, runtime.GOARCH, archiveSHA, o.cfg.PeerZone); err != nil {
		return "", 0, 0, fmt.Errorf("register: %w", err)
	}
	// Remember what we registered so GracefulShutdown can unregister it.
	o.registeredKey = key
	o.registeredEndpoint = selfEP
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
	peers, err := o.dir.ListPeersZoneAware(ctx, key, selfEP, o.cfg.PeerZone)
	if err != nil {
		return "", err
	}
	if o.cfg.PeerZone != "" {
		o.logf("directory has %d peer(s) (zone=%q first): %v", len(peers), o.cfg.PeerZone, peers)
	} else {
		o.logf("directory has %d peer(s): %v", len(peers), peers)
	}
	// Look up the etalon sha256 once. Missing (empty string) is OK for
	// archives registered before v0.11 — PullFromPeer just skips the check.
	etalon, _ := o.dir.ArchiveSHA256(ctx, key)
	dest := ArchivePath(o.cfg.ArchiveDir, key)
	for _, peer := range peers {
		o.logf("  trying pull: http://%s/archive/%s", peer, key)
		n, err := PullFromPeer(ctx, peer, key, dest, o.cfg.PeerPullTimeout, etalon)
		if err == nil {
			o.logf("  pulled %d bytes from %s (sha256 verified)", n, peer)
			return "pulled-from:" + peer, nil
		}
		o.logf("  pull failed: %v", err)
	}

	// Shorten the lock TTL and keep it alive with a heartbeat goroutine,
	// so if this primer dies mid-build the lock vanishes quickly instead
	// of blocking the cluster for o.cfg.BuildLockTTL.
	lockTTL := 60 * time.Second
	got, err := o.dir.TryAcquireBuildLock(ctx, key, selfEP, lockTTL)
	if err != nil {
		return "", fmt.Errorf("build lock: %w", err)
	}
	if got {
		o.logf("acquired build lock (TTL %s, renew every %s) — building locally",
			lockTTL, lockTTL/3)

		// Renew loop. Exits as soon as the build returns and we release.
		renewCtx, cancelRenew := context.WithCancel(ctx)
		defer cancelRenew()
		go func() {
			tick := time.NewTicker(lockTTL / 3)
			defer tick.Stop()
			for {
				select {
				case <-renewCtx.Done():
					return
				case <-tick.C:
					ok, err := o.dir.RenewBuildLock(renewCtx, key, selfEP, lockTTL)
					if err != nil {
						o.logf("  build lock renew: %v", err)
					} else if !ok {
						o.logf("  build lock lost (taken by someone else?)")
						return
					}
				}
			}
		}()

		buildErr := BuildLocally(ctx, o.profile, o.cfg.AppJar, o.cfg.ExtractDir, dest)
		cancelRenew()
		// Always release on exit — success or failure — so other primers
		// don't have to wait out the TTL.
		if relErr := o.dir.ReleaseBuildLock(context.Background(), key, selfEP); relErr != nil {
			o.logf("  build lock release: %v", relErr)
		}
		if buildErr != nil {
			return "", fmt.Errorf("build: %w", buildErr)
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
		peers, err := o.dir.ListPeersZoneAware(ctx, key, selfEP, o.cfg.PeerZone)
		if err != nil {
			continue
		}
		etalon, _ := o.dir.ArchiveSHA256(ctx, key)
		for _, peer := range peers {
			n, err := PullFromPeer(ctx, peer, key, dest, o.cfg.PeerPullTimeout, etalon)
			if err == nil {
				o.logf("  pulled-after-wait %d bytes from %s", n, peer)
				return "pulled-after-wait:" + peer, nil
			}
		}
	}
	return "", fmt.Errorf("timeout waiting for peer to publish key=%s", key)
}
