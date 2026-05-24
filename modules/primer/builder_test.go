package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadProfileValid(t *testing.T) {
	path := writeProfile(t, `apiVersion: classcache.dev/v1
kind: AgentProfile
metadata:
  name: scouter
  version: "2.21.3"
spec:
  agent:
    jar: /work/agent.jar
    config: /opt/scouter/conf/scouter.conf
  build:
    javaagent: true
    bootclasspath: true
    extraJvmOpts: [-Dscouter.config=/opt/scouter/conf/scouter.conf]
  runtime:
    javaagent: false
    bootclasspath: true
    extraJvmOpts: [-Dscouter.config=/opt/scouter/conf/scouter.conf]
`)
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Metadata.Name != "scouter" {
		t.Errorf("Name = %s", p.Metadata.Name)
	}
	if !p.Spec.Build.Javaagent {
		t.Error("expected build.javaagent=true")
	}
	if p.Spec.Runtime.Javaagent {
		t.Error("expected runtime.javaagent=false")
	}
}

func TestLoadProfileRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "wrong apiVersion",
			body: "apiVersion: other/v1\nkind: AgentProfile\nmetadata: {name: x}\nspec: {agent: {jar: /j}}\n",
			want: "AgentProfile",
		},
		{
			name: "missing name",
			body: "apiVersion: classcache.dev/v1\nkind: AgentProfile\nmetadata: {}\nspec: {agent: {jar: /j}}\n",
			want: "metadata.name",
		},
		{
			name: "missing agent jar",
			body: "apiVersion: classcache.dev/v1\nkind: AgentProfile\nmetadata: {name: x}\nspec: {agent: {}}\n",
			want: "spec.agent.jar",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProfile(t, tc.body)
			_, err := LoadProfile(path)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		want    []string // substrings that must all appear in order
	}{
		{
			name: "scouter-like hybrid build",
			profile: Profile{
				Spec: struct {
					Agent struct {
						Jar    string `yaml:"jar"`
						Config string `yaml:"config"`
					} `yaml:"agent"`
					Build   PhaseFlags `yaml:"build"`
					Runtime PhaseFlags `yaml:"runtime"`
				}{
					Agent: struct {
						Jar    string `yaml:"jar"`
						Config string `yaml:"config"`
					}{Jar: "/work/agent.jar"},
					Build: PhaseFlags{
						Javaagent:     true,
						Bootclasspath: true,
						ExtraJvmOpts:  []string{"-Dscouter.config=/x"},
					},
				},
			},
			want: []string{
				"-Xbootclasspath/a:/work/agent.jar",
				"-XX:ArchiveClassesAtExit=/arch/k.jsa",
				"-javaagent:/work/agent.jar",
				"-Dscouter.config=/x",
				"-jar", "/work/extracted/app.jar",
			},
		},
		{
			name: "no boot no agent",
			profile: Profile{
				Spec: struct {
					Agent struct {
						Jar    string `yaml:"jar"`
						Config string `yaml:"config"`
					} `yaml:"agent"`
					Build   PhaseFlags `yaml:"build"`
					Runtime PhaseFlags `yaml:"runtime"`
				}{
					Agent: struct {
						Jar    string `yaml:"jar"`
						Config string `yaml:"config"`
					}{Jar: "/work/agent.jar"},
					Build: PhaseFlags{
						Javaagent:     false,
						Bootclasspath: false,
					},
				},
			},
			want: []string{
				"-XX:ArchiveClassesAtExit=/arch/k.jsa",
				"-jar", "/work/extracted/app.jar",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := BuildArgs(&tc.profile, "/arch/k.jsa", "/work/extracted/app.jar")
			joined := strings.Join(args, " ")
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("missing %q in args: %v", w, args)
				}
			}
			// bootclasspath false must NOT appear
			if !tc.profile.Spec.Build.Bootclasspath {
				if strings.Contains(joined, "-Xbootclasspath/a:") {
					t.Errorf("unexpected bootclasspath in args: %v", args)
				}
			}
			if !tc.profile.Spec.Build.Javaagent {
				if strings.Contains(joined, "-javaagent:") {
					t.Errorf("unexpected javaagent in args: %v", args)
				}
			}
		})
	}
}
