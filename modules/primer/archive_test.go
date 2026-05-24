package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSha256File(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name)
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := sha256File(p)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestArchivePath(t *testing.T) {
	if got := ArchivePath("/var/lib/classcache", "abc123"); got != "/var/lib/classcache/abc123.jsa" {
		t.Errorf("ArchivePath = %s", got)
	}
}

func TestLocalArchiveExists(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		key     string
		setup   func(t *testing.T)
		want    bool
	}{
		{
			name: "missing",
			key:  "missing",
			setup: func(t *testing.T) {},
			want: false,
		},
		{
			name: "empty file",
			key:  "empty",
			setup: func(t *testing.T) {
				if err := os.WriteFile(ArchivePath(dir, "empty"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			name: "valid",
			key:  "valid",
			setup: func(t *testing.T) {
				if err := os.WriteFile(ArchivePath(dir, "valid"), []byte("data"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			if got := LocalArchiveExists(dir, tc.key); got != tc.want {
				t.Errorf("LocalArchiveExists(%s) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
