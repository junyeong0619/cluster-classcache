package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Directory wraps a Valkey/Redis client (wire-compatible) and exposes the
// directory primitives primer needs: peer set, build-lock, event publish.
type Directory struct {
	cli *redis.Client
}

func NewDirectory(host string, port int) *Directory {
	return &Directory{
		cli: redis.NewClient(&redis.Options{
			Addr: fmt.Sprintf("%s:%d", host, port),
		}),
	}
}

func newDirectoryWithClient(cli *redis.Client) *Directory {
	return &Directory{cli: cli}
}

func (d *Directory) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := d.cli.Ping(ctx).Err(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("valkey unreachable after %s", timeout)
}

func (d *Directory) Register(ctx context.Context, key, endpoint string, sizeBytes int64, jvm, arch, sha256, zone string) error {
	pipe := d.cli.Pipeline()
	pipe.HSet(ctx, "archive:"+key, map[string]any{
		"size":          sizeBytes,
		"registered_at": time.Now().Unix(),
		"jvm":           jvm,
		"arch":          arch,
		"sha256":        sha256,
	})
	pipe.SAdd(ctx, "archive:"+key+":peers", endpoint)
	// Zone is optional — kept in a parallel HSET so the existing peer set
	// representation (plain "host:port" strings) doesn't change. Empty
	// zone means "unknown", and unknown peers sort last after known ones
	// in the same zone as the caller.
	if zone != "" {
		pipe.HSet(ctx, "archive:"+key+":peer-zone", endpoint, zone)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// peerZoneMap returns the endpoint → zone map for an archive (or an empty
// map if no peer ever recorded a zone). Used by zone-aware ListPeers.
func (d *Directory) peerZoneMap(ctx context.Context, key string) (map[string]string, error) {
	m, err := d.cli.HGetAll(ctx, "archive:"+key+":peer-zone").Result()
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ArchiveSHA256 returns the etalon sha256 hex stored in Valkey for this
// archive, or "" if not set (legacy archives registered before v0.11).
func (d *Directory) ArchiveSHA256(ctx context.Context, key string) (string, error) {
	v, err := d.cli.HGet(ctx, "archive:"+key, "sha256").Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

// ListPeers returns endpoints in the peer set for this key, excluding selfEP.
func (d *Directory) ListPeers(ctx context.Context, key, selfEP string) ([]string, error) {
	members, err := d.cli.SMembers(ctx, "archive:"+key+":peers").Result()
	if err != nil {
		return nil, err
	}
	out := members[:0]
	for _, m := range members {
		if m != selfEP {
			out = append(out, m)
		}
	}
	return out, nil
}

// ListPeersZoneAware returns peers split into "same zone as selfZone" first,
// then "different zone or unknown" in arbitrary order. The point is to
// concentrate the cluster-wide rollout fan-out within a zone (intra-AZ
// bandwidth is free on most cloud providers) before crossing AZs.
//
// If selfZone is empty, behaves like ListPeers (no preference).
func (d *Directory) ListPeersZoneAware(ctx context.Context, key, selfEP, selfZone string) ([]string, error) {
	all, err := d.ListPeers(ctx, key, selfEP)
	if err != nil || selfZone == "" {
		return all, err
	}
	zones, err := d.peerZoneMap(ctx, key)
	if err != nil {
		// Soft failure — return unsorted list rather than blocking pulls.
		return all, nil
	}
	var same, other []string
	for _, ep := range all {
		if zones[ep] == selfZone {
			same = append(same, ep)
		} else {
			other = append(other, ep)
		}
	}
	return append(same, other...), nil
}

// TryAcquireBuildLock returns true if this caller now holds the build lock.
func (d *Directory) TryAcquireBuildLock(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	return d.cli.SetNX(ctx, "archive:"+key+":build_lock", holder, ttl).Result()
}

// RenewBuildLock extends the lock TTL but ONLY if `holder` still owns it.
// Returns true if the lock was extended. Use this from a goroutine while
// the build runs so a long build doesn't lose its lock to the TTL.
func (d *Directory) RenewBuildLock(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	// Lua: if GET == holder then EXPIRE ; else NO-OP. Atomic.
	const script = `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("PEXPIRE", KEYS[1], ARGV[2])
		else
			return 0
		end
	`
	res, err := d.cli.Eval(ctx, script,
		[]string{"archive:" + key + ":build_lock"},
		holder, int64(ttl/time.Millisecond)).Result()
	if err != nil {
		return false, err
	}
	n, _ := res.(int64)
	return n == 1, nil
}

// ReleaseBuildLock deletes the lock ONLY if this caller still owns it
// (don't yank someone else's lock if our TTL already expired).
func (d *Directory) ReleaseBuildLock(ctx context.Context, key, holder string) error {
	const script = `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		else
			return 0
		end
	`
	return d.cli.Eval(ctx, script,
		[]string{"archive:" + key + ":build_lock"}, holder).Err()
}

// Unregister removes this endpoint from the peer set on graceful shutdown.
// On crash we can't run this; stale peers are removed lazily by callers
// that fail to reach them.
func (d *Directory) Unregister(ctx context.Context, key, endpoint string) error {
	return d.cli.SRem(ctx, "archive:"+key+":peers", endpoint).Err()
}

type PrimerEvent struct {
	Node        string `json:"node"`
	Key         string `json:"key"`
	Method      string `json:"method"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	ArchiveSize int64  `json:"archive_size"`
}

func (d *Directory) PublishEvent(ctx context.Context, ev PrimerEvent) error {
	payload, _ := json.Marshal(ev)
	return d.cli.Publish(ctx, "primer-events", string(payload)).Err()
}

func (d *Directory) Close() error { return d.cli.Close() }

// FormatEndpoint joins host:port the way other primer code expects.
func FormatEndpoint(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}
