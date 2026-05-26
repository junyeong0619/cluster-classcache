package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// PeerServer serves archives over HTTP at /archive/{key}.
type PeerServer struct {
	ArchiveDir string
	Port       int

	srv *http.Server
}

func (p *PeerServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/archive/", p.handleArchive)
	p.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", p.Port),
		Handler: mux,
	}
	ln, err := net.Listen("tcp", p.srv.Addr)
	if err != nil {
		return err
	}
	go func() {
		_ = p.srv.Serve(ln)
	}()
	return nil
}

func (p *PeerServer) Shutdown(ctx context.Context) error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Shutdown(ctx)
}

func (p *PeerServer) handleArchive(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/archive/")
	if key == "" || strings.Contains(key, "/") {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	path := ArchivePath(p.ArchiveDir, key)
	st, err := os.Stat(path)
	if err != nil {
		http.Error(w, "archive not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
	_, _ = io.Copy(w, f)
}

// PullFromPeer downloads /archive/{key} from peer (host:port) to dest. Returns
// the bytes written. On failure dest is removed so a partial file doesn't
// masquerade as a hit.
//
// If `expectedSHA256` is non-empty, the downloaded file is verified against
// it after the body is fully written, and rejected on mismatch (the bad
// file is removed and an error is returned). This is integrity, not
// authenticity — the etalon hash itself comes from the same Valkey that
// peer addresses come from. PKI-style signing is a future task; even so,
// this catches transit corruption and stale-peer / wrong-archive serves.
func PullFromPeer(ctx context.Context, peer, key, dest string, timeout time.Duration, expectedSHA256 string) (int64, error) {
	url := "http://" + peer + "/archive/" + key
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	cli := &http.Client{Timeout: timeout}
	resp, err := cli.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("peer %s returned %s", peer, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(dest)
		return 0, err
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return 0, closeErr
	}
	if expectedSHA256 != "" {
		got, hashErr := sha256File(dest)
		if hashErr != nil {
			_ = os.Remove(dest)
			return 0, fmt.Errorf("verify after pull: %w", hashErr)
		}
		if got != expectedSHA256 {
			_ = os.Remove(dest)
			return 0, fmt.Errorf("sha256 mismatch from %s: got %s, want %s",
				peer, got, expectedSHA256)
		}
	}
	return n, nil
}
