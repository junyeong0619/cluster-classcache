package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

// reconcileValkey ensures a Valkey Deployment + Service exist when
// spec.valkey.create=true. Returns the host:port the primer should dial.
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

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cc.Namespace,
			Labels:    valkeyLabels(cc),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: valkeyLabels(cc)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: valkeyLabels(cc)},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "valkey",
						Image: image,
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
					}},
				},
			},
		},
	}
	if err := ctrl.SetControllerReference(cc, dep, r.Scheme); err != nil {
		return "", err
	}
	if err := upsert(ctx, r.Client, dep, &appsv1.Deployment{}); err != nil {
		return "", fmt.Errorf("valkey deployment: %w", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cc.Namespace,
			Labels:    valkeyLabels(cc),
		},
		Spec: corev1.ServiceSpec{
			Selector: valkeyLabels(cc),
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

	return fmt.Sprintf("%s.%s.svc.cluster.local:6379", name, cc.Namespace), nil
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
	case errors.IsNotFound(err):
		return c.Create(ctx, target)
	case err != nil:
		return err
	}
	target.SetResourceVersion(existing.GetResourceVersion())
	return c.Update(ctx, target)
}

func int32Ptr(v int32) *int32 { return &v }
