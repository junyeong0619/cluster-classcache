// App backend exposed to the React renderer.
//
// We deliberately shell out to kubectl + valkey-cli (same pattern as the C
// CLI). It keeps this binary dependency-free for the kubeconfig parsing /
// TLS / OIDC token machinery — kubectl already handles all of that — and
// matches what every demo script already runs.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type App struct {
	ctx context.Context
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// ─────────────────────────────────────────────────────────────────────────────
//  Data shapes returned to the frontend
// ─────────────────────────────────────────────────────────────────────────────

type ClassCacheSummary struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	WorkloadName string `json:"workloadName"`
	Profile      string `json:"profile"`
	Phase        string `json:"phase"`
	ArchiveKey   string `json:"archiveKey"`
}

type ArchiveSummary struct {
	Key            string   `json:"key"`
	SizeBytes      uint64   `json:"sizeBytes"`
	PeerEndpoints  []string `json:"peerEndpoints"`
	JVM            string   `json:"jvm"`
	Arch           string   `json:"arch"`
}

type SavingsSnapshot struct {
	Timestamp    int64  `json:"timestamp"`     // unix seconds
	TotalRssKiB  uint64 `json:"totalRssKiB"`
	TotalPssKiB  uint64 `json:"totalPssKiB"`
	SavedKiB     uint64 `json:"savedKiB"`
	SharedClean  uint64 `json:"sharedCleanKiB"`
	JVMs         int    `json:"jvms"`
}

type Diag struct {
	KubectlOK       bool   `json:"kubectlOK"`
	KubectlContext  string `json:"kubectlContext"`
	ValkeyAddr      string `json:"valkeyAddr"`
	ValkeyReachable bool   `json:"valkeyReachable"`
	Note            string `json:"note"`
}

// ─────────────────────────────────────────────────────────────────────────────
//  Helpers
// ─────────────────────────────────────────────────────────────────────────────

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

// ─────────────────────────────────────────────────────────────────────────────
//  Public API (auto-bound to window.go.main.App.* in the frontend)
// ─────────────────────────────────────────────────────────────────────────────

// Diagnose checks that kubectl and Valkey are reachable. Called once on app
// startup so the renderer can render a helpful banner if not.
func (a *App) Diagnose(valkeyHost string, valkeyPort int) Diag {
	d := Diag{
		ValkeyAddr: fmt.Sprintf("%s:%d", valkeyHost, valkeyPort),
	}
	if out, err := run("kubectl", "config", "current-context"); err == nil {
		d.KubectlOK = true
		d.KubectlContext = strings.TrimSpace(out)
	} else {
		d.Note = "kubectl not configured: " + err.Error()
	}
	if _, err := run("valkey-cli", "-h", valkeyHost, "-p", strconv.Itoa(valkeyPort), "PING"); err == nil {
		d.ValkeyReachable = true
	} else {
		// hiredis-cli might be missing; fall back to redis-cli.
		if _, err := run("redis-cli", "-h", valkeyHost, "-p", strconv.Itoa(valkeyPort), "PING"); err == nil {
			d.ValkeyReachable = true
		}
	}
	return d
}

// ListClassCaches calls `kubectl get classcaches -A -o json` and projects the
// minimal fields the UI needs.
func (a *App) ListClassCaches() ([]ClassCacheSummary, error) {
	out, err := run("kubectl", "get", "classcaches.classcache.dev", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Profile     string `json:"profile"`
				WorkloadRef struct {
					Name string `json:"name"`
				} `json:"workloadRef"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				ArchiveKey string `json:"archiveKey"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, err
	}
	res := make([]ClassCacheSummary, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		res = append(res, ClassCacheSummary{
			Name:         it.Metadata.Name,
			Namespace:    it.Metadata.Namespace,
			WorkloadName: it.Spec.WorkloadRef.Name,
			Profile:      it.Spec.Profile,
			Phase:        it.Status.Phase,
			ArchiveKey:   it.Status.ArchiveKey,
		})
	}
	return res, nil
}

// vk runs `valkey-cli -h HOST -p PORT <args...>` and returns trimmed stdout.
// Falls back to redis-cli for environments without valkey-cli installed.
func vk(host string, port int, args ...string) (string, error) {
	base := []string{"-h", host, "-p", strconv.Itoa(port)}
	full := append(base, args...)
	if out, err := run("valkey-cli", full...); err == nil {
		return strings.TrimRight(out, "\n"), nil
	}
	out, err := run("redis-cli", full...)
	return strings.TrimRight(out, "\n"), err
}

// ListArchives queries Valkey for archive metadata.
func (a *App) ListArchives(host string, port int) ([]ArchiveSummary, error) {
	keysOut, err := vk(host, port, "--raw", "KEYS", "archive:*")
	if err != nil {
		return nil, err
	}
	res := []ArchiveSummary{}
	for _, line := range strings.Split(keysOut, "\n") {
		line = strings.TrimSpace(line)
		// We want plain "archive:<16-hex>" — skip ":peers", ":build_lock", ":peer-zone".
		if !strings.HasPrefix(line, "archive:") || strings.Count(line, ":") != 1 {
			continue
		}
		key := strings.TrimPrefix(line, "archive:")
		if len(key) != 16 {
			continue
		}
		s := ArchiveSummary{Key: key}

		if hgetall, err := vk(host, port, "--raw", "HGETALL", "archive:"+key); err == nil {
			lines := strings.Split(hgetall, "\n")
			for i := 0; i+1 < len(lines); i += 2 {
				switch lines[i] {
				case "size":
					s.SizeBytes, _ = strconv.ParseUint(lines[i+1], 10, 64)
				case "jvm":
					s.JVM = lines[i+1]
				case "arch":
					s.Arch = lines[i+1]
				}
			}
		}
		if peers, err := vk(host, port, "--raw", "SMEMBERS", "archive:"+key+":peers"); err == nil {
			for _, p := range strings.Split(peers, "\n") {
				p = strings.TrimSpace(p)
				if p != "" {
					s.PeerEndpoints = append(s.PeerEndpoints, p)
				}
			}
		}
		res = append(res, s)
	}
	return res, nil
}

// SampleSavingsFromKind walks all workload pods that are scheduled on kind
// node containers and sums their archive-VMA smaps. Returns one snapshot
// with the current timestamp.
//
// This is the "kind only" fast path. A future addition will fall back to
// kubectl-exec the way modules/cli/src/stats.c already does.
func (a *App) SampleSavingsFromKind() (SavingsSnapshot, error) {
	snap := SavingsSnapshot{Timestamp: time.Now().Unix()}

	// 1. Find all node names where workload pods (labelled by the operator) live.
	out, err := run("kubectl", "get", "pod", "-A",
		"-l", "classcache.dev/managed-by",
		"-o", "jsonpath={range .items[*]}{.spec.nodeName}{\"\\n\"}{end}")
	if err != nil {
		return snap, err
	}
	nodeSet := map[string]struct{}{}
	for _, n := range strings.Split(out, "\n") {
		n = strings.TrimSpace(n)
		if n != "" {
			nodeSet[n] = struct{}{}
		}
	}

	for node := range nodeSet {
		pidsOut, err := run("docker", "exec", node, "pgrep", "-f", "/work/extracted/app.jar")
		if err != nil {
			continue
		}
		for _, pidStr := range strings.Split(strings.TrimSpace(pidsOut), "\n") {
			if pidStr == "" {
				continue
			}
			smapsOut, err := run("docker", "exec", node, "cat", "/proc/"+pidStr+"/smaps")
			if err != nil {
				continue
			}
			accumulateSmaps(smapsOut, &snap)
		}
	}
	if snap.TotalRssKiB > snap.TotalPssKiB {
		snap.SavedKiB = snap.TotalRssKiB - snap.TotalPssKiB
	}
	return snap, nil
}

// accumulateSmaps does the same parse as modules/cli/src/smaps.c but inside
// the Wails backend.
func accumulateSmaps(out string, snap *SavingsSnapshot) {
	inBlock := false
	hadAny := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "-") && strings.Contains(line, ".jsa") && strings.IndexByte(line, '-') < strings.IndexByte(line, ' ') {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.HasPrefix(line, "VmFlags:") {
			inBlock = false
			continue
		}
		// "Rss:                4 kB" → take the number.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "Rss:":
			snap.TotalRssKiB += val
			hadAny = true
		case "Pss:":
			snap.TotalPssKiB += val
		case "Shared_Clean:":
			snap.SharedClean += val
		}
	}
	if hadAny {
		snap.JVMs++
	}
}
