package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClassCacheSpec describes a single class-cache deployment: which workload to
// accelerate, which AgentProfile to use, where Valkey lives, and which primer
// image to roll out as a DaemonSet.
type ClassCacheSpec struct {
	// WorkloadRef points at the Deployment whose Pods should boot from the
	// archive. Operator owns its PodTemplate via initContainer + JVM opts.
	WorkloadRef WorkloadRef `json:"workloadRef"`

	// App and Agent are v0.9 additions: instead of baking everything into a
	// custom primer image, the operator now extracts app.jar and agent.jar
	// from the user's own image and a catalog agent image at Pod start.
	// If both App and Agent are set, the operator uses the universal primer
	// image (spec.primerImage default) and ignores any app baked into it.
	App   AppSpec   `json:"app,omitempty"`
	Agent AgentSpec `json:"agent,omitempty"`

	// Profile is the AgentProfile name. The operator materializes it into a
	// ConfigMap (looking up the YAML from the in-cluster profile catalog
	// ConfigMap, classcache-profiles in classcache-system) and mounts it
	// at /etc/classcache/profile.yaml inside the primer Pod.
	Profile string `json:"profile"`

	// ProfileYAML is an inline AgentProfile body. When set, takes precedence
	// over Profile (no catalog lookup, no extra ConfigMap).
	ProfileYAML string `json:"profileYAML,omitempty"`

	// PrimerImage is the container image to run on every worker node.
	// In v0.9 the universal image is the default and rarely needs override.
	// +kubebuilder:default="classcache-primer:v0.9-universal"
	PrimerImage string `json:"primerImage,omitempty"`

	// ArchiveDir is the hostPath on each worker where archives are cached.
	// +kubebuilder:default="/var/lib/classcache"
	ArchiveDir string `json:"archiveDir,omitempty"`

	// Valkey controls how the directory service is provisioned for this
	// ClassCache. If Create is true the operator manages a Valkey Deployment
	// + Service. Otherwise Addr (host:port) must point at an existing one.
	Valkey ValkeySpec `json:"valkey,omitempty"`

	// PatchMode controls how the operator integrates with the workload.
	//   "Owned"   — operator patches the Deployment PodTemplate directly.
	//   "Webhook" — operator only sets the cc.classcache.dev/inject label
	//               and the MutatingWebhook does the actual pod-time patch.
	// +kubebuilder:default=Owned
	// +kubebuilder:validation:Enum=Owned;Webhook
	PatchMode string `json:"patchMode,omitempty"`
}

// AppSpec points at the user's existing application image and the location
// of the Spring Boot fat jar inside it. The operator runs that image as an
// initContainer that just copies the jar into a shared emptyDir.
type AppSpec struct {
	// Image, e.g. "my-app:1.0" — exactly what your Deployment uses.
	Image string `json:"image,omitempty"`

	// JarPath inside Image. Default: /app.jar.
	// +kubebuilder:default="/app.jar"
	JarPath string `json:"jarPath,omitempty"`

	// ExtractorImage is an optional companion image used as the
	// initContainer source instead of Image. Use this when Image itself is
	// distroless (no shell, no java) and therefore can't host the extractor
	// step. ExtractorImage must contain `sh`, `cp`, and `java`, and must
	// contain the same fat jar at JarPath.
	//
	// When set, the workload Deployment still uses Image, but the operator
	// runs ExtractorImage in the cc-extract-app initContainer.
	ExtractorImage string `json:"extractorImage,omitempty"`
}

// AgentSpec points at a catalog APM-agent image (e.g.
// classcache-agent-scouter:v2.21) that ships the agent jar + optional conf
// file. The operator runs it as an initContainer too.
type AgentSpec struct {
	Image string `json:"image,omitempty"`

	// JarPath inside Image. Default: /agent.jar.
	// +kubebuilder:default="/agent.jar"
	JarPath string `json:"jarPath,omitempty"`

	// ConfigPath is optional — used by agents like Scouter that need an
	// external .conf file (-Dscouter.config=...). Default: empty.
	ConfigPath string `json:"configPath,omitempty"`
}

type WorkloadRef struct {
	// +kubebuilder:default="apps/v1"
	APIVersion string `json:"apiVersion,omitempty"`
	// +kubebuilder:default="Deployment"
	Kind string `json:"kind,omitempty"`
	Name string `json:"name"`
}

type ValkeySpec struct {
	// +kubebuilder:default=true
	Create bool `json:"create,omitempty"`
	// Addr is host:port; required when Create=false.
	Addr string `json:"addr,omitempty"`
	// Image is used only when Create=true.
	// +kubebuilder:default="valkey/valkey:7.2-alpine"
	Image string `json:"image,omitempty"`
}

type ClassCacheStatus struct {
	// ArchiveKey is the 16-char key the primer computed from
	// sha256(app||agent||jvm||arch||profile). Workloads use it to wait on
	// the right archive file. Populated once at least one primer has
	// published READY.
	ArchiveKey string `json:"archiveKey,omitempty"`

	// Phase: Pending | PrimerReady | WorkloadPatched | Ready | Failed
	Phase string `json:"phase,omitempty"`

	// ReadyPeers is the number of primer pods that have registered an
	// archive in the directory (best-effort observation).
	ReadyPeers int32 `json:"readyPeers,omitempty"`

	// LastError surfaces the most recent reconciliation failure so users
	// can `kubectl describe classcache` and see what went wrong.
	LastError string `json:"lastError,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadRef.name`
// +kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profile`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Key",type=string,JSONPath=`.status.archiveKey`

// ClassCache is the Schema for the classcaches API.
type ClassCache struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClassCacheSpec   `json:"spec,omitempty"`
	Status ClassCacheStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ClassCacheList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClassCache `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClassCache{}, &ClassCacheList{})
}
