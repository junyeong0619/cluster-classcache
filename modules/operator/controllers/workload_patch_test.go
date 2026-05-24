package controllers

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func basePT() *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "app",
				Image: "my-app:1",
			}},
		},
	}
}

func sampleSpec() PatchSpec {
	return PatchSpec{
		ArchiveDir:   "/var/lib/classcache",
		ArchiveKey:   "abc1234567890def",
		Profile:      "scouter",
		ClassCacheNS: "default",
		CCName:       "my-app",
	}
}

func TestApplyPodPatch_FreshTemplate(t *testing.T) {
	pt := basePT()
	if !ApplyPodPatch(pt, sampleSpec()) {
		t.Fatal("expected changed=true on fresh template")
	}

	// volume
	found := false
	for _, v := range pt.Spec.Volumes {
		if v.Name == cacheVolumeName {
			if v.HostPath == nil || v.HostPath.Path != "/var/lib/classcache" {
				t.Errorf("hostPath wrong: %+v", v.HostPath)
			}
			found = true
		}
	}
	if !found {
		t.Error("cache volume not added")
	}

	// init container
	if len(pt.Spec.InitContainers) != 1 || pt.Spec.InitContainers[0].Name != initContainerName {
		t.Errorf("init container missing: %+v", pt.Spec.InitContainers)
	}
	if !strings.Contains(pt.Spec.InitContainers[0].Command[2], "abc1234567890def.jsa") {
		t.Errorf("wait script doesn't mention archive key: %s", pt.Spec.InitContainers[0].Command[2])
	}

	// workload mount + env
	c := pt.Spec.Containers[0]
	if !containsMount(c.VolumeMounts, cacheVolumeName) {
		t.Error("workload container missing volume mount")
	}
	env := getEnv(c.Env, "CLASSCACHE_JAVA_OPTS")
	if env == nil {
		t.Fatal("CLASSCACHE_JAVA_OPTS env missing")
	}
	for _, sub := range []string{
		"-XX:SharedArchiveFile=/var/lib/classcache/abc1234567890def.jsa",
		"-XX:ArchiveRelocationMode=0",
		"-Xshare:on",
	} {
		if !strings.Contains(env.Value, sub) {
			t.Errorf("env missing %q: %s", sub, env.Value)
		}
	}
}

func TestApplyPodPatch_Idempotent(t *testing.T) {
	pt := basePT()
	ApplyPodPatch(pt, sampleSpec())
	if ApplyPodPatch(pt, sampleSpec()) {
		t.Error("second apply should report no change")
	}
}

func TestApplyPodPatch_UpdatesInitOnKeyChange(t *testing.T) {
	pt := basePT()
	ApplyPodPatch(pt, sampleSpec())

	spec2 := sampleSpec()
	spec2.ArchiveKey = "rotated9876543210"
	// Re-apply with different key — the init container command changes.
	// (BuildArgs / env will be updated too; check init script specifically.)
	pt.Spec.InitContainers[0].Command = []string{"sh", "-c", "OLD"}
	changed := ApplyPodPatch(pt, spec2)
	if !changed {
		t.Error("expected change after init script rotation")
	}
	if !strings.Contains(pt.Spec.InitContainers[0].Command[2], "rotated9876543210.jsa") {
		t.Errorf("init script not updated: %s", pt.Spec.InitContainers[0].Command[2])
	}
}

func TestApplyPodPatch_MultiContainer(t *testing.T) {
	pt := basePT()
	pt.Spec.Containers = append(pt.Spec.Containers, corev1.Container{
		Name: "sidecar", Image: "log-tailer:1",
	})
	ApplyPodPatch(pt, sampleSpec())

	for _, c := range pt.Spec.Containers {
		if !containsMount(c.VolumeMounts, cacheVolumeName) {
			t.Errorf("container %s missing mount", c.Name)
		}
		if getEnv(c.Env, "CLASSCACHE_JAVA_OPTS") == nil {
			t.Errorf("container %s missing env", c.Name)
		}
	}
}

func containsMount(vms []corev1.VolumeMount, name string) bool {
	for _, vm := range vms {
		if vm.Name == name {
			return true
		}
	}
	return false
}

func getEnv(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i, e := range env {
		if e.Name == name {
			return &env[i]
		}
	}
	return nil
}
