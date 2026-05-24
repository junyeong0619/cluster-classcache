package controllers

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

func newTestReconciler(t *testing.T, objs ...client.Object) *ClassCacheReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(classcachev1.AddToScheme(scheme))
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&classcachev1.ClassCache{}).
		Build()
	return &ClassCacheReconciler{Client: cli, Scheme: scheme}
}

func sampleCC() *classcachev1.ClassCache {
	return &classcachev1.ClassCache{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-app", Namespace: "default",
		},
		Spec: classcachev1.ClassCacheSpec{
			WorkloadRef: classcachev1.WorkloadRef{Name: "my-app"},
			Profile:     "scouter",
			ProfileYAML: "apiVersion: classcache.dev/v1\nkind: AgentProfile\nmetadata: {name: scouter}\nspec: {agent: {jar: /opt/agent/agent.jar}}\n",
			Valkey:      classcachev1.ValkeySpec{Create: true},
		},
	}
}

func TestReconcile_CreatesValkeyAndPrimer(t *testing.T) {
	cc := sampleCC()
	r := newTestReconciler(t, cc)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cc.Name, Namespace: cc.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	dep := &appsv1.Deployment{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Name: valkeyName(cc), Namespace: cc.Namespace}, dep); err != nil {
		t.Errorf("valkey deployment missing: %v", err)
	}
	if got := dep.Spec.Template.Spec.Containers[0].Image; got != "valkey/valkey:7.2-alpine" {
		t.Errorf("valkey image = %s", got)
	}

	ds := &appsv1.DaemonSet{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Name: primerName(cc), Namespace: cc.Namespace}, ds); err != nil {
		t.Errorf("primer daemonset missing: %v", err)
	}
	envs := ds.Spec.Template.Spec.Containers[0].Env
	if !envHas(envs, "VALKEY_HOST") || !envHas(envs, "VALKEY_PORT") {
		t.Errorf("primer missing VALKEY_HOST/PORT env: %+v", envs)
	}
}

func TestReconcile_DoesNotPatchUntilKeyPublished(t *testing.T) {
	cc := sampleCC() // Status.ArchiveKey == ""
	wl := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "my-app"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "my-app"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "my-app:1"}},
				},
			},
		},
	}
	r := newTestReconciler(t, cc, wl)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cc.Name, Namespace: cc.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &appsv1.Deployment{}
	_ = r.Get(context.Background(),
		types.NamespacedName{Name: "my-app", Namespace: "default"}, got)
	if hasInjectedSidecar(got) {
		t.Error("workload should NOT be patched before primer publishes archiveKey")
	}
}

func TestReconcile_PatchesAfterKeyPublished(t *testing.T) {
	cc := sampleCC()
	cc.Status.ArchiveKey = "deadbeefcafebabe"
	wl := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "my-app"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "my-app"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "my-app:1"}},
				},
			},
		},
	}
	r := newTestReconciler(t, cc, wl)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cc.Name, Namespace: cc.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &appsv1.Deployment{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Name: "my-app", Namespace: "default"}, got); err != nil {
		t.Fatal(err)
	}
	if !hasInjectedSidecar(got) {
		t.Errorf("workload not patched after key published: %+v", got.Spec.Template.Spec.InitContainers)
	}
	if got.Labels[managedByLabel] != cc.Name {
		t.Errorf("managed-by label not set")
	}
	env := got.Spec.Template.Spec.Containers[0].Env
	for _, e := range env {
		if e.Name == "CLASSCACHE_JAVA_OPTS" && !strings.Contains(e.Value, "deadbeefcafebabe") {
			t.Errorf("env should use real key, got %s", e.Value)
		}
	}
}

func TestReconcile_ValkeyExternal(t *testing.T) {
	cc := sampleCC()
	cc.Spec.Valkey = classcachev1.ValkeySpec{Create: false, Addr: "external-valkey:6379"}
	r := newTestReconciler(t, cc)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cc.Name, Namespace: cc.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ds := &appsv1.DaemonSet{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Name: primerName(cc), Namespace: cc.Namespace}, ds); err != nil {
		t.Fatal(err)
	}
	envs := ds.Spec.Template.Spec.Containers[0].Env
	host := envValue(envs, "VALKEY_HOST")
	port := envValue(envs, "VALKEY_PORT")
	if host != "external-valkey" || port != "6379" {
		t.Errorf("expected external valkey host:port, got %s:%s", host, port)
	}
	// And no Deployment was created
	dep := &appsv1.Deployment{}
	err = r.Get(context.Background(),
		types.NamespacedName{Name: valkeyName(cc), Namespace: cc.Namespace}, dep)
	if err == nil {
		t.Error("expected no valkey deployment when create=false")
	}
}

func TestSplitAddr(t *testing.T) {
	cases := []struct {
		in         string
		host, port string
	}{
		{"valkey.cc.svc:6379", "valkey.cc.svc", "6379"},
		{"10.0.0.1:6380", "10.0.0.1", "6380"},
		{"nohost", "nohost", "6379"},
	}
	for _, tc := range cases {
		h, p := splitAddr(tc.in)
		if h != tc.host || p != tc.port {
			t.Errorf("splitAddr(%q) = %s:%s, want %s:%s", tc.in, h, p, tc.host, tc.port)
		}
	}
}

func envHas(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}
