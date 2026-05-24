package webhook

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

func setup(t *testing.T, cc *classcachev1.ClassCache) *PodMutator {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(classcachev1.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if cc != nil {
		builder = builder.WithObjects(cc)
	}
	cli := builder.Build()
	return NewPodMutator(cli, admission.NewDecoder(scheme))
}

func podRequest(t *testing.T, pod *corev1.Pod) admission.Request {
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object: runtime.RawExtension{Raw: raw},
		},
	}
}

func TestHandle_NoLabelAllowed(t *testing.T) {
	m := setup(t, nil)
	resp := m.Handle(context.Background(), podRequest(t, &corev1.Pod{}))
	if !resp.Allowed {
		t.Errorf("expected Allowed for unlabelled pod, got %+v", resp)
	}
}

func TestHandle_MissingCC(t *testing.T) {
	m := setup(t, nil)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels:    map[string]string{InjectLabel: "ghost"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}},
	}
	resp := m.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed {
		t.Errorf("expected Allowed when CC missing, got %+v", resp)
	}
}

func TestHandle_OwnedModeSkipped(t *testing.T) {
	cc := &classcachev1.ClassCache{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
		Spec: classcachev1.ClassCacheSpec{
			WorkloadRef: classcachev1.WorkloadRef{Name: "x"},
			Profile:     "scouter",
			PatchMode:   "Owned",
			ArchiveDir:  "/var/lib/classcache",
		},
	}
	m := setup(t, cc)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels:    map[string]string{InjectLabel: "x"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}},
	}
	resp := m.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed {
		t.Errorf("expected Allowed when patchMode=Owned, got %+v", resp)
	}
	if len(resp.Patches) != 0 {
		t.Errorf("expected zero patches in Owned mode, got %d", len(resp.Patches))
	}
}

func TestHandle_WebhookModeWaitsUntilKey(t *testing.T) {
	cc := &classcachev1.ClassCache{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
		Spec: classcachev1.ClassCacheSpec{
			WorkloadRef: classcachev1.WorkloadRef{Name: "x"},
			Profile:     "scouter",
			PatchMode:   "Webhook",
			ArchiveDir:  "/var/lib/classcache",
		},
	}
	m := setup(t, cc)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels:    map[string]string{InjectLabel: "x"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}},
	}
	resp := m.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed {
		t.Fatalf("expected Allowed even without key, got %+v", resp)
	}
	if len(resp.Patches) != 0 {
		t.Error("expected NO patches before primer publishes archiveKey")
	}
}

func TestHandle_WebhookModePatchesWhenReady(t *testing.T) {
	cc := &classcachev1.ClassCache{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
		Spec: classcachev1.ClassCacheSpec{
			WorkloadRef: classcachev1.WorkloadRef{Name: "x"},
			Profile:     "scouter",
			PatchMode:   "Webhook",
			ArchiveDir:  "/var/lib/classcache",
		},
		Status: classcachev1.ClassCacheStatus{
			ArchiveKey: "deadbeefcafebabe",
		},
	}
	m := setup(t, cc)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels:    map[string]string{InjectLabel: "x"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}},
	}
	resp := m.Handle(context.Background(), podRequest(t, pod))
	if !resp.Allowed {
		t.Fatalf("expected Allowed, got %+v", resp)
	}
	if len(resp.Patches) == 0 {
		t.Error("expected JSON patches once key is set")
	}
}
