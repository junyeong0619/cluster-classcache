package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const keyLen = 16

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func jvmVersion() (string, error) {
	cmd := exec.Command("java", "-version")
	out, _ := cmd.CombinedOutput()
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if line == "" {
		return "", fmt.Errorf("java -version produced no output")
	}
	return line, nil
}

// ComputeKey hashes appJar + agentJar + jvmVer + arch + profileName into a
// 16-char prefix. profileName is included so two profiles against the same
// app/agent get distinct archives.
func ComputeKey(appJar, agentJar, profileName string) (string, error) {
	appHash, err := sha256File(appJar)
	if err != nil {
		return "", fmt.Errorf("hash app jar: %w", err)
	}
	agentHash, err := sha256File(agentJar)
	if err != nil {
		return "", fmt.Errorf("hash agent jar: %w", err)
	}
	jvm, err := jvmVersion()
	if err != nil {
		return "", err
	}
	full := sha256.Sum256([]byte(strings.Join([]string{
		appHash, agentHash, jvm, runtime.GOARCH, profileName,
	}, "|")))
	return hex.EncodeToString(full[:])[:keyLen], nil
}

func ArchivePath(dir, key string) string {
	return filepath.Join(dir, key+".jsa")
}

func LocalArchiveExists(dir, key string) bool {
	st, err := os.Stat(ArchivePath(dir, key))
	return err == nil && st.Size() > 0
}
