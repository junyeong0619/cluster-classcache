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
	Timestamp       int64  `json:"timestamp"`
	TotalSizeKiB    uint64 `json:"totalSizeKiB"`    // Σ VMA Size (claimed footprint)
	TotalRssKiB     uint64 `json:"totalRssKiB"`     // Σ Rss across all JVM archive VMAs
	TotalPssKiB     uint64 `json:"totalPssKiB"`     // Σ Pss (proportional share)
	SavedKiB        uint64 `json:"savedKiB"`        // Rss − Pss (memory de-duplicated by sharing)
	SharedCleanKiB  uint64 `json:"sharedCleanKiB"`  // mmap "hit" — backed by page cache, shared
	SharedDirtyKiB  uint64 `json:"sharedDirtyKiB"`
	PrivateCleanKiB uint64 `json:"privateCleanKiB"` // mmap "miss" — relocated / not page-cache-backed
	PrivateDirtyKiB uint64 `json:"privateDirtyKiB"` // mmap "miss" — JVM wrote to the page (CoW)
	JVMs            int    `json:"jvms"`
}

type PodStat struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Node      string `json:"node"`
	CPUMilli  int    `json:"cpuMilli"`
	MemMiB    int    `json:"memMiB"`
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

// run shells out and returns trimmed stdout. Declared as a var so tests can
// swap it for a canned-response fake without spinning up real kubectl / docker
// / valkey-cli processes.
var run = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

// ─────────────────────────────────────────────────────────────────────────────
//  Public API (auto-bound to window.go.main.App.* in the frontend)
// ─────────────────────────────────────────────────────────────────────────────

// Diagnose checks that kubectl and Valkey are reachable. The renderer calls
// it on startup AND whenever the Valkey host/port changes, so any failure
// reason is written to Note so the UI can show it.
func (a *App) Diagnose(valkeyHost string, valkeyPort int) Diag {
	d := Diag{
		ValkeyAddr: fmt.Sprintf("%s:%d", valkeyHost, valkeyPort),
	}
	var notes []string
	if out, err := run("kubectl", "config", "current-context"); err == nil {
		d.KubectlOK = true
		d.KubectlContext = strings.TrimSpace(out)
	} else {
		notes = append(notes, "kubectl not configured: "+err.Error())
	}
	if _, err := run("valkey-cli", "-h", valkeyHost, "-p", strconv.Itoa(valkeyPort), "PING"); err == nil {
		d.ValkeyReachable = true
	} else {
		// valkey-cli might be missing; fall back to redis-cli.
		if _, err2 := run("redis-cli", "-h", valkeyHost, "-p", strconv.Itoa(valkeyPort), "PING"); err2 == nil {
			d.ValkeyReachable = true
		} else {
			notes = append(notes, fmt.Sprintf("valkey unreachable at %s (valkey-cli: %v; redis-cli: %v)", d.ValkeyAddr, err, err2))
		}
	}
	d.Note = strings.Join(notes, " · ")
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

// SampleSavings walks every workload pod (labelled by the operator) and
// sums the smaps counters for its archive-mmap VMAs. Returns one snapshot
// with the current timestamp.
//
// Two paths:
//   - kind fast path: `docker exec <node> pgrep + cat /proc/<pid>/smaps`,
//     one call per node, picks up every JVM on that node at once.
//   - kubectl exec fallback: for nodes where docker exec fails (k3d,
//     hosted clusters, anything where the kubelet host isn't a docker
//     container we can reach), we fall back to per-pod `kubectl exec
//     POD -- cat /proc/1/smaps`. Slower but works anywhere.
func (a *App) SampleSavings() (SavingsSnapshot, error) {
	snap := SavingsSnapshot{Timestamp: time.Now().Unix()}

	// List all workload pods with (namespace, name, nodeName) tuples.
	out, err := run("kubectl", "get", "pod", "-A",
		"-l", "classcache.dev/managed-by",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}{\"|\"}{.metadata.name}{\"|\"}{.spec.nodeName}{\"\\n\"}{end}")
	if err != nil {
		return snap, err
	}
	type podID struct{ ns, name, node string }
	var pods []podID
	nodeSet := map[string]struct{}{}
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(ln, "|")
		if len(parts) != 3 || parts[1] == "" {
			continue
		}
		pods = append(pods, podID{parts[0], parts[1], parts[2]})
		nodeSet[parts[2]] = struct{}{}
	}

	// Fast path: one `docker exec` per node. Track which nodes responded
	// so we know which pods still need the slow kubectl-exec fallback.
	nodeOK := map[string]bool{}
	for node := range nodeSet {
		pidsOut, err := run("docker", "exec", node, "pgrep", "-f", "/work/extracted/app.jar")
		if err != nil {
			continue
		}
		nodeOK[node] = true
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

	// Slow path: for pods on nodes where docker exec didn't work, exec
	// straight into the container. PID 1 inside the workload pod is the JVM.
	for _, p := range pods {
		if nodeOK[p.node] {
			continue
		}
		smapsOut, err := run("kubectl", "exec", "-n", p.ns, p.name, "--", "cat", "/proc/1/smaps")
		if err != nil {
			continue
		}
		accumulateSmaps(smapsOut, &snap)
	}

	if snap.TotalRssKiB > snap.TotalPssKiB {
		snap.SavedKiB = snap.TotalRssKiB - snap.TotalPssKiB
	}
	return snap, nil
}

// accumulateSmaps does the same parse as modules/cli/src/smaps.c but inside
// the Wails backend. We sum every kB-tagged counter the smaps header gives
// us — the renderer derives hit/miss from these.
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
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "Size:":
			snap.TotalSizeKiB += val
		case "Rss:":
			snap.TotalRssKiB += val
			hadAny = true
		case "Pss:":
			snap.TotalPssKiB += val
		case "Shared_Clean:":
			snap.SharedCleanKiB += val
		case "Shared_Dirty:":
			snap.SharedDirtyKiB += val
		case "Private_Clean:":
			snap.PrivateCleanKiB += val
		case "Private_Dirty:":
			snap.PrivateDirtyKiB += val
		}
	}
	if hadAny {
		snap.JVMs++
	}
}

// PodStats lists CPU/mem per workload pod via `kubectl top`. Requires
// metrics-server in the cluster; returns ([], nil) when it's not installed
// so the UI can degrade gracefully.
func (a *App) PodStats() ([]PodStat, error) {
	out, err := run("kubectl", "get", "pod", "-A",
		"-l", "classcache.dev/managed-by",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}{\"|\"}{.metadata.name}{\"|\"}{.spec.nodeName}{\"\\n\"}{end}")
	if err != nil {
		return nil, err
	}
	type id struct{ ns, name, node string }
	var pods []id
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(ln, "|")
		if len(parts) != 3 {
			continue
		}
		pods = append(pods, id{parts[0], parts[1], parts[2]})
	}

	// Build a (ns, name) → metrics map from `kubectl top`. One call per
	// namespace keeps this dead-simple; if metrics-server isn't there,
	// the command errors and we return what we have (zeroed metrics).
	nsSet := map[string]struct{}{}
	for _, p := range pods {
		nsSet[p.ns] = struct{}{}
	}
	type m struct{ cpu, mem int }
	metrics := map[string]m{}
	for ns := range nsSet {
		topOut, terr := run("kubectl", "top", "pod", "-n", ns, "--no-headers")
		if terr != nil {
			continue
		}
		for _, ln := range strings.Split(topOut, "\n") {
			f := strings.Fields(ln)
			if len(f) < 3 {
				continue
			}
			metrics[ns+"|"+f[0]] = m{cpu: parseCPU(f[1]), mem: parseMem(f[2])}
		}
	}

	res := make([]PodStat, 0, len(pods))
	for _, p := range pods {
		mm := metrics[p.ns+"|"+p.name]
		res = append(res, PodStat{
			Namespace: p.ns, Name: p.name, Node: p.node,
			CPUMilli: mm.cpu, MemMiB: mm.mem,
		})
	}
	return res, nil
}

// "100m" → 100, "1" → 1000, "1500m" → 1500.
func parseCPU(s string) int {
	if strings.HasSuffix(s, "m") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "m"))
		return v
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(v * 1000)
}

// "256Mi" → 256, "1Gi" → 1024, "512Ki" → 0 (rounded down).
func parseMem(s string) int {
	mult := 1
	num := s
	switch {
	case strings.HasSuffix(s, "Gi"):
		mult, num = 1024, strings.TrimSuffix(s, "Gi")
	case strings.HasSuffix(s, "Mi"):
		mult, num = 1, strings.TrimSuffix(s, "Mi")
	case strings.HasSuffix(s, "Ki"):
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "Ki"))
		return v / 1024
	}
	v, _ := strconv.Atoi(num)
	return v * mult
}
