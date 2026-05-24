package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// StatusPublisher posts the real archive key to a ClassCache CR's
// /status subresource via merge-patch. It uses the pod's in-cluster
// ServiceAccount token directly so no client-go dependency is needed —
// keeps the primer image footprint at ~80MB.
type StatusPublisher struct {
	apiBase    string
	namespace  string
	name       string
	token      string
	httpClient *http.Client
}

const (
	saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// NewStatusPublisher returns nil, nil if the primer isn't running in a Pod
// (e.g., docker-compose). Callers should treat that as "publishing disabled".
func NewStatusPublisher(namespace, name string) (*StatusPublisher, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, nil
	}
	tokenBytes, err := os.ReadFile(saTokenPath)
	if err != nil {
		return nil, fmt.Errorf("read SA token: %w", err)
	}
	caBytes, err := os.ReadFile(saCAPath)
	if err != nil {
		return nil, fmt.Errorf("read SA CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("no CA certs parsed from %s", saCAPath)
	}
	return &StatusPublisher{
		apiBase:   fmt.Sprintf("https://%s:%s", host, port),
		namespace: namespace,
		name:      name,
		token:     string(tokenBytes),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		},
	}, nil
}

// PublishArchiveKey merge-patches .status.archiveKey on the ClassCache.
// Idempotent: re-publishing the same key is a no-op on the server side.
func (p *StatusPublisher) PublishArchiveKey(ctx context.Context, key string, archiveSize int64) error {
	if p == nil {
		return nil
	}
	url := fmt.Sprintf("%s/apis/classcache.dev/v1/namespaces/%s/classcaches/%s/status",
		p.apiBase, p.namespace, p.name)
	body, _ := json.Marshal(map[string]any{
		"status": map[string]any{
			"archiveKey": key,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PATCH status %s: %s", resp.Status, string(b))
	}
	return nil
}
