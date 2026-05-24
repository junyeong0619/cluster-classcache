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
