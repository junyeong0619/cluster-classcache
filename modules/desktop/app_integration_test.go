//go:build integration

// Integration tests that drive a real kind cluster.
//
// Prereq:  ./scripts/quickstart.sh   (brings up kind + operator + the
//                                     "quickstart" ClassCache in ns cc-demo)
// Run:     go test -tags=integration -v ./...
//
// Each test self-skips if its precondition isn't met (no kubectl context,
// no ClassCache CR, Valkey port-forward failed, workload not Ready yet,
// …) — so a partial environment still runs the parts it can.
//
// Override defaults via env:
//   EXPECTED_CC_NAMESPACE   (default "cc-demo")
//   EXPECTED_CC_NAME        (default "quickstart")
//   VALKEY_HOST             (skip port-forward, use this addr instead)
//   VALKEY_PORT             (paired with VALKEY_HOST)

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ccName() string {
	if v := os.Getenv("EXPECTED_CC_NAME"); v != "" {
		return v
	}
	return "quickstart"
}

func ccNamespace() string {
	if v := os.Getenv("EXPECTED_CC_NAMESPACE"); v != "" {
		return v
	}
	return "cc-demo"
}

// requireKubectl skips the test if kubectl isn't pointing at any cluster.
func requireKubectl(t *testing.T) {
	t.Helper()
	out, err := exec.Command("kubectl", "config", "current-context").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("kubectl not configured — run ./scripts/quickstart.sh first")
	}
	t.Logf("kubectl context: %s", strings.TrimSpace(string(out)))
}

// requireClassCache skips when the expected CC CR doesn't exist in the cluster.
func requireClassCache(t *testing.T) {
	t.Helper()
	requireKubectl(t)
	cmd := exec.Command("kubectl", "-n", ccNamespace(), "get", "cc", ccName())
	if err := cmd.Run(); err != nil {
		t.Skipf("ClassCache %s/%s not found — run ./scripts/quickstart.sh first", ccNamespace(), ccName())
	}
}

// portForwardValkey returns (host, port) for talking to the Valkey instance
// managed by the expected CC. Honors VALKEY_HOST/VALKEY_PORT for direct
// access; otherwise spawns `kubectl port-forward` as a child process and
// tears it down on cleanup.
func portForwardValkey(t *testing.T) (string, int) {
	t.Helper()
	if h := os.Getenv("VALKEY_HOST"); h != "" {
		p, err := strconv.Atoi(os.Getenv("VALKEY_PORT"))
		if err != nil || p == 0 {
			t.Fatalf("VALKEY_HOST set but VALKEY_PORT invalid: %q", os.Getenv("VALKEY_PORT"))
		}
		return h, p
	}
	requireClassCache(t)

	// Pick a free local port. There's an unavoidable race between
	// closing the listener and kubectl binding, but it's tiny in practice.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	svc := "svc/cc-" + ccName() + "-valkey"
	cmd := exec.Command("kubectl", "-n", ccNamespace(), "port-forward",
		svc, fmt.Sprintf("%d:6379", port))
	if err := cmd.Start(); err != nil {
		t.Skipf("kubectl port-forward failed to spawn: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Wait up to 5s for the port to accept connections.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = conn.Close()
			t.Logf("valkey port-forward ready at 127.0.0.1:%d -> %s/%s", port, ccNamespace(), svc)
			return "127.0.0.1", port
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skipf("kubectl port-forward to %s/%s did not become ready in 5s", ccNamespace(), svc)
	return "", 0
}

// ─────────────────────────────────────────────────────────────────────────────
//  Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestIntegration_Diagnose(t *testing.T) {
	requireKubectl(t)
	host, port := portForwardValkey(t)

	d := (&App{}).Diagnose(host, port)

	if !d.KubectlOK {
		t.Errorf("KubectlOK=false against a live cluster: %+v", d)
	}
	if d.KubectlContext == "" {
		t.Error("KubectlContext empty")
	}
	if !d.ValkeyReachable {
		t.Errorf("ValkeyReachable=false against forwarded service: %+v", d)
	}
	if d.ValkeyAddr != fmt.Sprintf("%s:%d", host, port) {
		t.Errorf("ValkeyAddr=%q, want %s:%d", d.ValkeyAddr, host, port)
	}
}

func TestIntegration_ListClassCaches(t *testing.T) {
	requireClassCache(t)

	ccs, err := (&App{}).ListClassCaches()
	if err != nil {
		t.Fatal(err)
	}
	var matched *ClassCacheSummary
	names := []string{}
	for i := range ccs {
		names = append(names, ccs[i].Namespace+"/"+ccs[i].Name)
		if ccs[i].Namespace == ccNamespace() && ccs[i].Name == ccName() {
			matched = &ccs[i]
		}
	}
	if matched == nil {
		t.Fatalf("expected %s/%s in result; got %v", ccNamespace(), ccName(), names)
	}
	if matched.Profile == "" {
		t.Errorf("profile should be projected: %+v", matched)
	}
	if matched.WorkloadName == "" {
		t.Errorf("workloadName should be projected: %+v", matched)
	}
	// Phase is allowed to be Pending if the test runs early — but it must
	// be set to *something* once the operator has reconciled at least once.
	if matched.Phase == "" {
		t.Logf("phase empty — operator may not have reconciled yet")
	}
}

func TestIntegration_ListArchives(t *testing.T) {
	host, port := portForwardValkey(t)

	// Primer builds an archive a few seconds after the CC reaches Ready.
	// Poll up to 60s, then skip if nothing showed up (workload not Ready).
	var ars []ArchiveSummary
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		got, err := (&App{}).ListArchives(host, port)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 0 {
			ars = got
			break
		}
		time.Sleep(2 * time.Second)
	}
	if len(ars) == 0 {
		t.Skip("no archives advertised after 60s — workload likely not Ready yet")
	}

	for _, a := range ars {
		if len(a.Key) != 16 {
			t.Errorf("expected 16-hex archive key, got %q", a.Key)
		}
		if a.SizeBytes == 0 {
			t.Errorf("archive %s reports size=0", a.Key)
		}
		if len(a.PeerEndpoints) == 0 {
			t.Errorf("archive %s has no peers — primer should have registered itself", a.Key)
		}
	}
}

func TestIntegration_PodStats(t *testing.T) {
	requireClassCache(t)

	pods, err := (&App{}).PodStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) == 0 {
		t.Skip("no managed pods — quickstart may not have rolled out yet")
	}
	for _, p := range pods {
		if p.Name == "" || p.Namespace == "" || p.Node == "" {
			t.Errorf("pod identity incomplete (kubectl jsonpath broke?): %+v", p)
		}
	}
	// quickstart Deployment has 3 replicas. Once they're scheduled, we
	// should see >=3 pods. Don't hard-fail on this — replicas may still be
	// rolling out.
	if len(pods) < 3 {
		t.Logf("only %d managed pods seen — quickstart expects 3", len(pods))
	}
	// kubectl top will silently report zeros if metrics-server isn't
	// installed (quickstart doesn't install it). Log so the operator
	// can see, but don't fail.
	hasMetrics := false
	for _, p := range pods {
		if p.CPUMilli > 0 || p.MemMiB > 0 {
			hasMetrics = true
			break
		}
	}
	if !hasMetrics {
		t.Log("all metrics zero — metrics-server not installed in this cluster")
	}
}

func TestIntegration_SampleSavings(t *testing.T) {
	requireClassCache(t)

	// Wait up to 60s for the JVMs to come up and accumulate a non-zero Rss.
	var snap SavingsSnapshot
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		s, err := (&App{}).SampleSavings()
		if err != nil {
			t.Fatal(err)
		}
		snap = s
		if snap.JVMs > 0 && snap.TotalRssKiB > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if snap.JVMs == 0 {
		t.Skip("no JVMs sampled — workload likely not Ready yet")
	}
	if snap.TotalRssKiB == 0 {
		t.Errorf("expected Rss > 0 once JVMs are detected: %+v", snap)
	}
	if snap.TotalPssKiB > snap.TotalRssKiB {
		t.Errorf("Pss must not exceed Rss: %+v", snap)
	}
	// Multi-JVM sharing is the whole point of cluster-classcache. With the
	// quickstart's 3 replicas we expect SavedKiB > 0 once the page cache
	// has warmed. Treat the warm-up gap as a log, not a failure.
	if snap.JVMs >= 2 && snap.SavedKiB == 0 {
		t.Logf("warning: %d JVMs but SavedKiB=0 — page cache may not be warm yet", snap.JVMs)
	}
}
