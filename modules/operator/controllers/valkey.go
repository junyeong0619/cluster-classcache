package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

// reconcileValkey ensures a Valkey StatefulSet + headless Service exist when
// spec.valkey.create=true. Returns the host:port the primer should dial.
//
// v0.12: changed from Deployment-with-emptyDir to StatefulSet with a PVC +
// AOF persistence. A simple Pod restart (eviction, OOM, node drain → reschedule
// on the same node) no longer loses the directory, which used to force every
// primer in the cluster to re-register and triggered a spurious build storm
// when ListPeers came back empty mid-transition.
//
// HA (multi-replica, automatic failover) is still future work. AOF + PVC
// closes the "Pod bounce" failure mode; node-loss-with-PVC is a StorageClass
// concern (use a network-attached SC like EBS gp3 / pd-ssd).
func (r *ClassCacheReconciler) reconcileValkey(ctx context.Context, cc *classcachev1.ClassCache) (string, error) {
	if !cc.Spec.Valkey.Create {
		if cc.Spec.Valkey.Addr == "" {
			return "", fmt.Errorf("valkey.create=false but valkey.addr is empty")
		}
		return cc.Spec.Valkey.Addr, nil
	}

	name := valkeyName(cc)
	image := cc.Spec.Valkey.Image
	if image == "" {
		image = "valkey/valkey:7.2-alpine"
	}
	storageSize := cc.Spec.Valkey.StorageSize
	if storageSize == "" {
		storageSize = "256Mi"
	}
	storageQty, err := resource.ParseQuantity(storageSize)
	if err != nil {
		return "", fmt.Errorf("valkey.storageSize %q invalid: %w", storageSize, err)
	}

	// Best-effort migration: an older Deployment created in v0.11 must be
	// removed before we can create a StatefulSet with the same name.
	if err := r.deleteLegacyDeployment(ctx, name, cc.Namespace); err != nil {
		return "", fmt.Errorf("valkey legacy cleanup: %w", err)
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cc.Namespace,
			Labels:    valkeyLabels(cc),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,                                          // headless service name
			Replicas:    int32Ptr(1),
			Selector:    &metav1.LabelSelector{MatchLabels: valkeyLabels(cc)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: valkeyLabels(cc)},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "valkey",
						Image: image,
						// AOF on, fsync every second — durability vs. throughput
						// tradeoff that matches our write rate (one HSet/SADD
						// per archive register, infrequent).
						Args: []string{
							"valkey-server",
							"--appendonly", "yes",
							"--appendfsync", "everysec",
							"--dir", "/data",
						},
						Ports: []corev1.ContainerPort{{Name: "valkey", ContainerPort: 6379}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								TCPSocket: &corev1.TCPSocketAction{
									Port: intstr.FromInt(6379),
								},
							},
							InitialDelaySeconds: 1,
							PeriodSeconds:       1,
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "data",
							MountPath: "/data",
						}},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "data",
					Labels: valkeyLabels(cc),
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: storageQty,
						},
					},
					StorageClassName: storageClassPtr(cc.Spec.Valkey.StorageClassName),
				},
			}},
		},
	}
	if err := ctrl.SetControllerReference(cc, sts, r.Scheme); err != nil {
		return "", err
	}
	if err := upsert(ctx, r.Client, sts, &appsv1.StatefulSet{}); err != nil {
		return "", fmt.Errorf("valkey statefulset: %w", err)
	}

	// Headless service for the StatefulSet, plus a regular ClusterIP service
	// alias so existing primer pods can keep dialing host:6379 unchanged.
	// (StatefulSet requires a headless service; we keep the historical
	// ClusterIP behavior by setting ClusterIP="" and a deterministic name.)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cc.Namespace,
			Labels:    valkeyLabels(cc),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,             // headless
			Selector:  valkeyLabels(cc),
			Ports: []corev1.ServicePort{{
				Port: 6379, TargetPort: intstr.FromInt(6379),
			}},
		},
	}
	if err := ctrl.SetControllerReference(cc, svc, r.Scheme); err != nil {
		return "", err
	}
	if err := upsert(ctx, r.Client, svc, &corev1.Service{}); err != nil {
		return "", fmt.Errorf("valkey service: %w", err)
	}

	// With a headless service backing a 1-replica StatefulSet, the addressable
	// host is the pod-DNS name (<sts-name>-0.<svc>.<ns>.svc.cluster.local).
	// The plain service DNS also resolves to that pod IP because there's
	// only one pod, so we keep returning the service-style address for
	// backward compatibility with primer env wiring.
	return fmt.Sprintf("%s.%s.svc.cluster.local:6379", name, cc.Namespace), nil
}

// deleteLegacyDeployment removes a v0.11-style Deployment with the same name
// as our new StatefulSet, if any. Required because k8s won't co-host two
// workloads on the same selector. Safe to call when the Deployment doesn't
// exist (NotFound is treated as success).
func (r *ClassCacheReconciler) deleteLegacyDeployment(ctx context.Context, name, ns string) error {
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, dep)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, dep)
}

func valkeyName(cc *classcachev1.ClassCache) string {
	return "cc-" + cc.Name + "-valkey"
}

func valkeyLabels(cc *classcachev1.ClassCache) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "valkey",
		"app.kubernetes.io/instance":   cc.Name,
		"app.kubernetes.io/managed-by": "classcache-operator",
	}
}

// upsert creates or updates target by reading the existing object into existing
// first and copying generation-independent fields. We use a server-side apply
// style: just SET the spec to what we want. ResourceVersion is preserved.
func upsert(ctx context.Context, c client.Client, target, existing client.Object) error {
	key := types.NamespacedName{Name: target.GetName(), Namespace: target.GetNamespace()}
	err := c.Get(ctx, key, existing)
	switch {
	case apierrors.IsNotFound(err):
		return c.Create(ctx, target)
	case err != nil:
		return err
	}
	target.SetResourceVersion(existing.GetResourceVersion())
	return c.Update(ctx, target)
}

func storageClassPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int32Ptr(v int32) *int32 { return &v }
