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

func (d *Directory) Register(ctx context.Context, key, endpoint string, sizeBytes int64, jvm, arch string) error {
	pipe := d.cli.Pipeline()
	pipe.HSet(ctx, "archive:"+key, map[string]any{
		"size":          sizeBytes,
		"registered_at": time.Now().Unix(),
		"jvm":           jvm,
		"arch":          arch,
	})
	pipe.SAdd(ctx, "archive:"+key+":peers", endpoint)
	_, err := pipe.Exec(ctx)
	return err
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
