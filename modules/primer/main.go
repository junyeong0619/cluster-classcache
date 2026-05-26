package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func loadConfig() Config {
	node := envOr("NODE_NAME", "")
	if node == "" {
		hn, _ := os.Hostname()
		node = hn
	}
	return Config{
		NodeName:           node,
		PeerHost:           envOr("PEER_HOST", node),
		PeerPort:           envInt("PEER_PORT", 8088),
		// PEER_ZONE is optional. If set (typically by the operator from a
		// node label like topology.kubernetes.io/zone), pulls prefer
		// same-zone peers before crossing AZs. v0.11 ships the protocol;
		// auto-population by the operator is v0.12.
		PeerZone:           os.Getenv("PEER_ZONE"),
		ArchiveDir:         envOr("ARCHIVE_DIR", "/var/lib/classcache"),
		AppJar:             os.Getenv("APP_JAR"),
		AgentJar:           os.Getenv("AGENT_JAR"),
		ExtractDir:         envOr("EXTRACT_DIR", "/work/extracted"),
		ProfilePath:        envOr("PROFILE_PATH", "/etc/classcache/profile.yaml"),
		BuildLockTTL:       10 * time.Minute,
		PeerPullTimeout:    60 * time.Second,
		WaitForPeerTimeout: 10 * time.Minute,
	}
}

func main() {
	log.SetFlags(0)
	cfg := loadConfig()
	if cfg.AppJar == "" || cfg.AgentJar == "" {
		log.Fatalf("FATAL: APP_JAR and AGENT_JAR must be set")
	}

	if err := os.MkdirAll(cfg.ArchiveDir, 0o755); err != nil {
		log.Fatalf("FATAL: mkdir archive dir: %v", err)
	}

	profile, err := LoadProfile(cfg.ProfilePath)
	if err != nil {
		log.Fatalf("FATAL: %v", err)
	}

	dir := NewDirectory(envOr("VALKEY_HOST", "redis"), envInt("VALKEY_PORT", 6379))
	defer dir.Close()

	peer := &PeerServer{ArchiveDir: cfg.ArchiveDir, Port: cfg.PeerPort}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	ccName := os.Getenv("CLASSCACHE_NAME")
	ccNamespace := os.Getenv("CLASSCACHE_NAMESPACE")
	var publisher *StatusPublisher
	if ccName != "" && ccNamespace != "" {
		p, err := NewStatusPublisher(ccNamespace, ccName)
		if err != nil {
			log.Printf("status publisher disabled: %v", err)
		} else {
			publisher = p
		}
	}

	orch := NewOrchestrator(cfg, profile, dir, publisher)
	if _, _, _, err := orch.Run(ctx, peer); err != nil {
		log.Fatalf("FATAL: %v", err)
	}

	<-ctx.Done()
	shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	orch.GracefulShutdown(shutdownCtx)
	_ = peer.Shutdown(shutdownCtx)
}
