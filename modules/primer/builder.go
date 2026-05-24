package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Profile is the in-memory representation of an AgentProfile YAML
// (modules/agent-profiles/schema/v1.json).
type Profile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
	} `yaml:"metadata"`
	Spec struct {
		Agent struct {
			Jar    string `yaml:"jar"`
			Config string `yaml:"config"`
		} `yaml:"agent"`
		Build   PhaseFlags `yaml:"build"`
		Runtime PhaseFlags `yaml:"runtime"`
	} `yaml:"spec"`
}

type PhaseFlags struct {
	Javaagent     bool     `yaml:"javaagent"`
	Bootclasspath bool     `yaml:"bootclasspath"`
	ExtraJvmOpts  []string `yaml:"extraJvmOpts"`
}

func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	if p.APIVersion != "classcache.dev/v1" || p.Kind != "AgentProfile" {
		return nil, fmt.Errorf("profile %s: not a classcache.dev/v1 AgentProfile", path)
	}
	if p.Metadata.Name == "" {
		return nil, fmt.Errorf("profile %s: metadata.name required", path)
	}
	if p.Spec.Agent.Jar == "" {
		return nil, fmt.Errorf("profile %s: spec.agent.jar required", path)
	}
	return &p, nil
}

// BuildArgs returns the JVM argv to invoke for the build phase: archive will
// be written to archivePath. extractedAppJar is the path to the exploded
// Spring Boot main jar.
func BuildArgs(p *Profile, archivePath, extractedAppJar string) []string {
	args := []string{}
	if p.Spec.Build.Bootclasspath {
		args = append(args, "-Xbootclasspath/a:"+p.Spec.Agent.Jar)
	}
	args = append(args,
		"-XX:+UnlockDiagnosticVMOptions",
		"-XX:+AllowArchivingWithJavaAgent",
		"-XX:ArchiveClassesAtExit="+archivePath,
	)
	if p.Spec.Build.Javaagent {
		args = append(args, "-javaagent:"+p.Spec.Agent.Jar)
	}
	args = append(args, p.Spec.Build.ExtraJvmOpts...)
	args = append(args, "-jar", extractedAppJar)
	return args
}

// ensureExtracted runs `java -Djarmode=tools -jar app.jar extract` once.
func ensureExtracted(appJar, extractDir string) error {
	marker := filepath.Join(extractDir, "app.jar")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("java", "-Djarmode=tools", "-jar", appJar,
		"extract", "--destination", extractDir)
	cmd.Dir = extractDir
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}

// BuildLocally runs the warmup JVM, hits two endpoints to exercise the app,
// then waits for the JVM to write the archive on exit.
func BuildLocally(ctx context.Context, p *Profile, appJar, extractDir, archivePath string) error {
	if err := ensureExtracted(appJar, extractDir); err != nil {
		return fmt.Errorf("extract spring-boot jar: %w", err)
	}
	args := BuildArgs(p, archivePath, filepath.Join(extractDir, "app.jar"))
	cmd := exec.CommandContext(ctx, "java", args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start jvm: %w", err)
	}

	if err := waitForHTTP(ctx, "http://localhost:8080/hello", 60*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("spring boot didn't start: %w", err)
	}
	hit(ctx, "http://localhost:8080/hello")
	hit(ctx, "http://localhost:8080/work/100")

	// SIGTERM so ArchiveClassesAtExit can flush the archive.
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	return nil
}

func waitForHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		cli := &http.Client{Timeout: time.Second}
		if resp, err := cli.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timeout after %s", timeout)
}

func hit(ctx context.Context, url string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	cli := &http.Client{Timeout: 5 * time.Second}
	if resp, err := cli.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}
