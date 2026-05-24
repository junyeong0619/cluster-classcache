package controllers

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

const (
	initContainerName = "cc-wait-archive"
	cacheVolumeName   = "cc-archive"
	archiveKeyAnnot   = "classcache.dev/archive-key"
	managedByLabel    = "classcache.dev/managed-by"
)

// PatchSpec captures the additions the operator (or the mutating webhook)
// applies to a Pod spec so that the workload boots from a shared archive.
// Keeping it as a struct lets us share the exact same logic between the
// reconciler and the webhook.
type PatchSpec struct {
	ArchiveDir   string
	ArchiveKey   string
	Profile      string
	ClassCacheNS string
	CCName       string
}

// reconcileWorkload fetches the referenced Deployment and patches its
// PodTemplate to mount the shared archive and pass the right JVM flags.
func (r *ClassCacheReconciler) reconcileWorkload(ctx context.Context, cc *classcachev1.ClassCache) error {
	if cc.Spec.WorkloadRef.Kind != "Deployment" {
		return fmt.Errorf("only Deployment workloadRef is supported in v0.7 (got %s)", cc.Spec.WorkloadRef.Kind)
	}
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Name: cc.Spec.WorkloadRef.Name, Namespace: cc.Namespace}
	if err := r.Get(ctx, key, dep); err != nil {
		if apierrors.IsNotFound(err) {
			// User declared a ClassCache before creating the workload —
			// that's fine; we'll patch it the next time the workload exists.
			return nil
		}
		return fmt.Errorf("get workload %s: %w", key, err)
	}

	spec := PatchSpec{
		ArchiveDir:   cc.Spec.ArchiveDir,
		ArchiveKey:   cc.Status.ArchiveKey,
		Profile:      cc.Spec.Profile,
		ClassCacheNS: cc.Namespace,
		CCName:       cc.Name,
	}
	if changed := ApplyPodPatch(&dep.Spec.Template, spec); !changed {
		return nil
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[archiveKeyAnnot] = spec.ArchiveKey
	if dep.Labels == nil {
		dep.Labels = map[string]string{}
	}
	dep.Labels[managedByLabel] = cc.Name
	return r.Update(ctx, dep)
}

// ApplyPodPatch mutates a PodTemplateSpec in place: adds initContainer,
// hostPath volume, mounts, and JVM env. Returns true if anything changed so
// callers can avoid a no-op write back to the API server.
//
// Idempotent: if the initContainer with our name is already there, we update
// it in place; if the volume is already there, we leave it; the JVM env is
// either set or left intact.
func ApplyPodPatch(pt *corev1.PodTemplateSpec, p PatchSpec) bool {
	changed := false
	changed = ensureVolume(pt, p) || changed
	changed = ensureInitContainer(pt, p) || changed
	changed = ensureWorkloadMountAndEnv(pt, p) || changed
	return changed
}

func ensureVolume(pt *corev1.PodTemplateSpec, p PatchSpec) bool {
	for _, v := range pt.Spec.Volumes {
		if v.Name == cacheVolumeName {
			return false
		}
	}
	pt.Spec.Volumes = append(pt.Spec.Volumes, corev1.Volume{
		Name: cacheVolumeName,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: p.ArchiveDir,
			Type: hostPathType(corev1.HostPathDirectoryOrCreate),
		}},
	})
	return true
}

func ensureInitContainer(pt *corev1.PodTemplateSpec, p PatchSpec) bool {
	desired := corev1.Container{
		Name:    initContainerName,
		Image:   "busybox:1.36",
		Command: []string{"sh", "-c", waitScript(p)},
		VolumeMounts: []corev1.VolumeMount{{
			Name: cacheVolumeName, MountPath: p.ArchiveDir, ReadOnly: true,
		}},
	}
	for i, ic := range pt.Spec.InitContainers {
		if ic.Name == initContainerName {
			if !containerEquivalent(ic, desired) {
				pt.Spec.InitContainers[i] = desired
				return true
			}
			return false
		}
	}
	pt.Spec.InitContainers = append([]corev1.Container{desired}, pt.Spec.InitContainers...)
	return true
}

func waitScript(p PatchSpec) string {
	// Wait up to 10 minutes for the archive file the primer is producing.
	// The key is propagated via env (set on the workload container as well).
	return fmt.Sprintf(
		`set -e; for i in $(seq 1 600); do [ -s %s/%s.jsa ] && exit 0; sleep 1; done; echo "archive %s/%s.jsa never appeared" >&2; exit 1`,
		p.ArchiveDir, p.ArchiveKey, p.ArchiveDir, p.ArchiveKey,
	)
}

func ensureWorkloadMountAndEnv(pt *corev1.PodTemplateSpec, p PatchSpec) bool {
	changed := false
	for i := range pt.Spec.Containers {
		c := &pt.Spec.Containers[i]
		if addMount(c, p) {
			changed = true
		}
		if addEnv(c, p) {
			changed = true
		}
	}
	return changed
}

func addMount(c *corev1.Container, p PatchSpec) bool {
	for _, vm := range c.VolumeMounts {
		if vm.Name == cacheVolumeName {
			return false
		}
	}
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: cacheVolumeName, MountPath: p.ArchiveDir, ReadOnly: true,
	})
	return true
}

func addEnv(c *corev1.Container, p PatchSpec) bool {
	opts := strings.Join([]string{
		"-XX:+UnlockDiagnosticVMOptions",
		"-XX:+AllowArchivingWithJavaAgent",
		"-XX:SharedArchiveFile=" + p.ArchiveDir + "/" + p.ArchiveKey + ".jsa",
		"-XX:ArchiveRelocationMode=0",
		"-Xshare:on",
	}, " ")

	for i, e := range c.Env {
		if e.Name == "CLASSCACHE_JAVA_OPTS" {
			if e.Value == opts {
				return false
			}
			c.Env[i].Value = opts
			return true
		}
	}
	c.Env = append(c.Env, corev1.EnvVar{Name: "CLASSCACHE_JAVA_OPTS", Value: opts})
	return true
}

func containerEquivalent(a, b corev1.Container) bool {
	if a.Image != b.Image || len(a.Command) != len(b.Command) {
		return false
	}
	for i := range a.Command {
		if a.Command[i] != b.Command[i] {
			return false
		}
	}
	return true
}
