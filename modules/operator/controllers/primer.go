package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

const (
	// in-Pod paths used both by the extractor initContainers and the
	// primer main container.
	workDir       = "/work"
	extractedDir  = "/work/extracted"
	stagedAppJar  = "/work/app.jar"
	stagedAgent   = "/opt/agent/agent.jar"
	stagedConf    = "/opt/agent/agent.conf"
	profileMount  = "/etc/classcache/profile.yaml"
	profileMountD = "/etc/classcache"

	appVolume     = "cc-app-jar"
	agentVolume   = "cc-agent"
	cacheVolume   = "cache"
	profileVolume = "cc-profile"
)

// reconcilePrimer creates the per-ClassCache primer DaemonSet. v0.9: app and
// agent jars are no longer baked into the primer image — they're extracted
// at Pod start from the user's app image and a catalog agent image, so the
// same primer image (classcache-primer:v0.9-universal) works for every
// ClassCache. The AgentProfile is mounted from a ConfigMap.
func (r *ClassCacheReconciler) reconcilePrimer(ctx context.Context, cc *classcachev1.ClassCache, valkeyAddr string) error {
	host, port := splitAddr(valkeyAddr)

	useExtractor := cc.Spec.App.Image != "" && cc.Spec.Agent.Image != ""

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      primerName(cc),
			Namespace: cc.Namespace,
			Labels:    primerLabels(cc),
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: primerLabels(cc)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: primerLabels(cc)},
				Spec:       primerPodSpec(cc, host, port, useExtractor),
			},
		},
	}
	if err := ctrl.SetControllerReference(cc, ds, r.Scheme); err != nil {
		return err
	}
	if err := upsert(ctx, r.Client, ds, &appsv1.DaemonSet{}); err != nil {
		return fmt.Errorf("primer daemonset: %w", err)
	}
	return nil
}

func primerPodSpec(cc *classcachev1.ClassCache, valkeyHost, valkeyPort string, useExtractor bool) corev1.PodSpec {
	env := []corev1.EnvVar{
		{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
		}},
		{Name: "PEER_HOST", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
		}},
		{Name: "VALKEY_HOST", Value: valkeyHost},
		{Name: "VALKEY_PORT", Value: valkeyPort},
		{Name: "PEER_PORT", Value: "8088"},
		{Name: "ARCHIVE_DIR", Value: cc.Spec.ArchiveDir},
		{Name: "CLASSCACHE_NAME", Value: cc.Name},
		{Name: "CLASSCACHE_NAMESPACE", Value: cc.Namespace},
		{Name: "PROFILE_PATH", Value: profileMount},
	}
	mounts := []corev1.VolumeMount{
		{Name: cacheVolume, MountPath: cc.Spec.ArchiveDir},
	}
	volumes := []corev1.Volume{
		{
			Name: cacheVolume,
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: cc.Spec.ArchiveDir,
				Type: hostPathType(corev1.HostPathDirectoryOrCreate),
			}},
		},
	}

	var initContainers []corev1.Container

	if useExtractor {
		env = append(env,
			corev1.EnvVar{Name: "APP_JAR", Value: "/work/app.jar"},
			corev1.EnvVar{Name: "AGENT_JAR", Value: "/opt/agent/agent.jar"},
			corev1.EnvVar{Name: "EXTRACT_DIR", Value: "/work/extracted"},
		)
		// In the primer container we want /work/app.jar and
		// /opt/agent/agent.jar — same volumes the extractors wrote to.
		mounts = append(mounts,
			corev1.VolumeMount{Name: appVolume, MountPath: "/work"},
			corev1.VolumeMount{Name: agentVolume, MountPath: "/opt/agent"},
		)
		volumes = append(volumes,
			corev1.Volume{Name: appVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			corev1.Volume{Name: agentVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		)

		appJarPath := cc.Spec.App.JarPath
		if appJarPath == "" {
			appJarPath = "/app.jar"
		}
		agentJarPath := cc.Spec.Agent.JarPath
		if agentJarPath == "" {
			agentJarPath = "/agent.jar"
		}

		// Mount the extractor's emptyDir at /cc-staging, NOT at /work, so we
		// don't shadow the source app jar that often lives under /work or /app.
		//
		// extractorImage overrides the source for the initContainer: when the
		// user's app image is distroless and lacks sh/cp/java, they can publish
		// a companion non-distroless image with the same jar and point
		// spec.app.extractorImage at it. The workload pod itself still uses
		// spec.app.image — only this init step swaps source.
		extractorImage := cc.Spec.App.ExtractorImage
		if extractorImage == "" {
			extractorImage = cc.Spec.App.Image
		}
		initContainers = append(initContainers, corev1.Container{
			Name:            "cc-extract-app",
			Image:           extractorImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c", extractAppScript(appJarPath)},
			VolumeMounts:    []corev1.VolumeMount{{Name: appVolume, MountPath: "/cc-staging"}},
		})

		agentInit := corev1.Container{
			Name:            "cc-extract-agent",
			Image:           cc.Spec.Agent.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c", extractAgentScript(agentJarPath, cc.Spec.Agent.ConfigPath)},
			VolumeMounts:    []corev1.VolumeMount{{Name: agentVolume, MountPath: "/cc-staging"}},
		}
		initContainers = append(initContainers, agentInit)
	}

	// Profile ConfigMap mount (always present in v0.9 — either inline or catalog).
	mounts = append(mounts, corev1.VolumeMount{
		Name: profileVolume, MountPath: profileMountD, ReadOnly: true,
	})
	volumes = append(volumes, corev1.Volume{
		Name: profileVolume,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: profileCMName(cc)},
				Items: []corev1.KeyToPath{{Key: "profile.yaml", Path: "profile.yaml"}},
			},
		},
	})

	return corev1.PodSpec{
		ServiceAccountName: primerSAName(cc),
		InitContainers:     initContainers,
		Containers: []corev1.Container{{
			Name:            "primer",
			Image:           cc.Spec.PrimerImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             env,
			Ports:           []corev1.ContainerPort{{Name: "peer", ContainerPort: 8088}},
			VolumeMounts:    mounts,
		}},
		Volumes: volumes,
	}
}

// extractAppScript runs in the user's app image. It writes into the shared
// emptyDir mounted at /cc-staging — never at /work/ or /app/, which would
// shadow the source jar in the user image.
func extractAppScript(jarPath string) string {
	return fmt.Sprintf(
		`set -e
cp %s /cc-staging/app.jar
mkdir -p /cc-staging/extracted
cd /cc-staging/extracted
java -Djarmode=tools -jar /cc-staging/app.jar extract --destination /cc-staging/extracted >/dev/null 2>&1 || \
  echo "warn: jarmode=tools extract failed — primer will retry at runtime" >&2
ls -la /cc-staging/app.jar
`, jarPath)
}

// extractAgentScript runs in the agent catalog image. Some agents are a
// single jar (Scouter, OTel) while others ship a multi-file directory
// (Pinpoint: bootstrap jar + lib/ + plugin/ + boot/ + conf). We detect
// which one at runtime: if `jarPath` resolves to a directory, copy it
// recursively into /cc-staging/agent and let the runtime profile point
// -javaagent at the bootstrap jar inside.
func extractAgentScript(jarPath, confPath string) string {
	s := "set -e\n"
	s += fmt.Sprintf(
		`if [ -d %[1]s ]; then
  mkdir -p /cc-staging/agent
  cp -a %[1]s/. /cc-staging/agent/
else
  cp %[1]s /cc-staging/agent.jar
fi
`, jarPath)
	if confPath != "" {
		s += fmt.Sprintf("[ -e %[1]s ] && cp %[1]s /cc-staging/agent.conf || true\n", confPath)
	}
	s += "ls -la /cc-staging/\n"
	return s
}

func primerName(cc *classcachev1.ClassCache) string {
	return "cc-" + cc.Name + "-primer"
}

func primerLabels(cc *classcachev1.ClassCache) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "primer",
		"app.kubernetes.io/instance":   cc.Name,
		"app.kubernetes.io/managed-by": "classcache-operator",
		"classcache.dev/owner":         cc.Name,
	}
}

func splitAddr(addr string) (host, port string) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:]
		}
	}
	return addr, "6379"
}

func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }
