package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

// reconcileProfileConfigMap materializes the chosen AgentProfile into a
// per-ClassCache ConfigMap and mounts it into the primer Pod at
// /etc/classcache/profile.yaml.
//
// Resolution order:
//  1. spec.profileYAML — inline, win.
//  2. spec.profile — looked up by key in the cluster-wide catalog ConfigMap
//     classcache-system/classcache-profiles.
func (r *ClassCacheReconciler) reconcileProfileConfigMap(ctx context.Context, cc *classcachev1.ClassCache) error {
	body, err := r.resolveProfileBody(ctx, cc)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      profileCMName(cc),
			Namespace: cc.Namespace,
			Labels:    primerLabels(cc),
		},
		Data: map[string]string{"profile.yaml": body},
	}
	if err := ctrl.SetControllerReference(cc, cm, r.Scheme); err != nil {
		return err
	}
	return upsert(ctx, r.Client, cm, &corev1.ConfigMap{})
}

func (r *ClassCacheReconciler) resolveProfileBody(ctx context.Context, cc *classcachev1.ClassCache) (string, error) {
	if cc.Spec.ProfileYAML != "" {
		return cc.Spec.ProfileYAML, nil
	}
	if cc.Spec.Profile == "" {
		return "", fmt.Errorf("either spec.profile or spec.profileYAML must be set")
	}
	catalog := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: "classcache-profiles", Namespace: "classcache-system"}
	if err := r.Get(ctx, key, catalog); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("profile catalog ConfigMap %s not found — install it or use spec.profileYAML", key)
		}
		return "", err
	}
	body, ok := catalog.Data[cc.Spec.Profile+".yaml"]
	if !ok {
		body, ok = catalog.Data[cc.Spec.Profile]
	}
	if !ok {
		return "", fmt.Errorf("profile %q not found in catalog %s (available: %v)", cc.Spec.Profile, key, mapKeys(catalog.Data))
	}
	return body, nil
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func profileCMName(cc *classcachev1.ClassCache) string {
	return "cc-" + cc.Name + "-profile"
}
