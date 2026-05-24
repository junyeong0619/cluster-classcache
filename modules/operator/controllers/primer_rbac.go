package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

// reconcilePrimerRBAC creates the ServiceAccount + Role + RoleBinding that
// lets the primer Pod patch its owning ClassCache's /status subresource.
// All three are namespaced and owned by the ClassCache, so deletion cascades.
func (r *ClassCacheReconciler) reconcilePrimerRBAC(ctx context.Context, cc *classcachev1.ClassCache) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      primerSAName(cc),
			Namespace: cc.Namespace,
			Labels:    primerLabels(cc),
		},
	}
	if err := ctrl.SetControllerReference(cc, sa, r.Scheme); err != nil {
		return err
	}
	if err := upsert(ctx, r.Client, sa, &corev1.ServiceAccount{}); err != nil {
		return fmt.Errorf("primer service account: %w", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      primerSAName(cc),
			Namespace: cc.Namespace,
			Labels:    primerLabels(cc),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"classcache.dev"},
				Resources:     []string{"classcaches/status"},
				ResourceNames: []string{cc.Name},
				Verbs:         []string{"get", "patch", "update"},
			},
			{
				APIGroups:     []string{"classcache.dev"},
				Resources:     []string{"classcaches"},
				ResourceNames: []string{cc.Name},
				Verbs:         []string{"get"},
			},
		},
	}
	if err := ctrl.SetControllerReference(cc, role, r.Scheme); err != nil {
		return err
	}
	if err := upsert(ctx, r.Client, role, &rbacv1.Role{}); err != nil {
		return fmt.Errorf("primer role: %w", err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      primerSAName(cc),
			Namespace: cc.Namespace,
			Labels:    primerLabels(cc),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     primerSAName(cc),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      primerSAName(cc),
			Namespace: cc.Namespace,
		}},
	}
	if err := ctrl.SetControllerReference(cc, rb, r.Scheme); err != nil {
		return err
	}
	if err := upsert(ctx, r.Client, rb, &rbacv1.RoleBinding{}); err != nil {
		return fmt.Errorf("primer rolebinding: %w", err)
	}
	return nil
}

func primerSAName(cc *classcachev1.ClassCache) string {
	return "cc-" + cc.Name + "-primer"
}
