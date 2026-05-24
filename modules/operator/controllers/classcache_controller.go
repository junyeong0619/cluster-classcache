package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
)

const (
	finalizerName = "classcache.dev/finalizer"

	PhasePending         = "Pending"
	PhasePrimerReady     = "PrimerReady"
	PhaseWorkloadPatched = "WorkloadPatched"
	PhaseReady           = "Ready"
	PhaseFailed          = "Failed"
)

type ClassCacheReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile is the entry point for the controller-runtime reconciliation loop.
// It is intentionally short: each phase is delegated to a dedicated method.
func (r *ClassCacheReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx).WithValues("classcache", req.NamespacedName)

	cc := &classcachev1.ClassCache{}
	if err := r.Get(ctx, req.NamespacedName, cc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	r.applyDefaults(cc)

	if !cc.DeletionTimestamp.IsZero() {
		// owner references handle cascade deletion of derived objects.
		return ctrl.Result{}, nil
	}

	valkeyAddr, err := r.reconcileValkey(ctx, cc)
	if err != nil {
		return r.setFailed(ctx, cc, err, lg)
	}

	if err := r.reconcilePrimerRBAC(ctx, cc); err != nil {
		return r.setFailed(ctx, cc, err, lg)
	}

	if err := r.reconcileProfileConfigMap(ctx, cc); err != nil {
		return r.setFailed(ctx, cc, err, lg)
	}

	if err := r.reconcilePrimer(ctx, cc, valkeyAddr); err != nil {
		return r.setFailed(ctx, cc, err, lg)
	}

	if cc.Spec.PatchMode == "Owned" && cc.Status.ArchiveKey != "" {
		if err := r.reconcileWorkload(ctx, cc); err != nil {
			return r.setFailed(ctx, cc, err, lg)
		}
	}

	phase, peers, err := r.observePhase(ctx, cc)
	if err != nil {
		lg.Info("phase observation degraded", "err", err)
	}

	cc.Status.Phase = phase
	cc.Status.ReadyPeers = peers
	cc.Status.LastError = ""
	// NOTE: ArchiveKey is intentionally NOT set here — the primer PATCHes
	// /status with the authoritative sha256(jars) once an archive is
	// registered. Overwriting it with a placeholder here would create a
	// reconcile/publish race.
	if err := r.Status().Update(ctx, cc); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ClassCacheReconciler) applyDefaults(cc *classcachev1.ClassCache) {
	if cc.Spec.PrimerImage == "" {
		cc.Spec.PrimerImage = "classcache-primer:v0.9-universal"
	}
	if cc.Spec.App.JarPath == "" {
		cc.Spec.App.JarPath = "/app.jar"
	}
	if cc.Spec.Agent.JarPath == "" {
		cc.Spec.Agent.JarPath = "/agent.jar"
	}
	if cc.Spec.ArchiveDir == "" {
		cc.Spec.ArchiveDir = "/var/lib/classcache"
	}
	if cc.Spec.PatchMode == "" {
		cc.Spec.PatchMode = "Owned"
	}
	if cc.Spec.WorkloadRef.APIVersion == "" {
		cc.Spec.WorkloadRef.APIVersion = "apps/v1"
	}
	if cc.Spec.WorkloadRef.Kind == "" {
		cc.Spec.WorkloadRef.Kind = "Deployment"
	}
}

func (r *ClassCacheReconciler) setFailed(ctx context.Context, cc *classcachev1.ClassCache, err error, lg interface{ Error(error, string, ...any) }) (ctrl.Result, error) {
	lg.Error(err, "reconcile failed")
	cc.Status.Phase = PhaseFailed
	cc.Status.LastError = err.Error()
	_ = r.Status().Update(ctx, cc)
	return ctrl.Result{}, err
}

// observePhase counts DaemonSet readiness as a stand-in for "how many primers
// have an archive registered". A status subresource on ClassCache that the
// primer itself updates is a v0.8 refinement.
func (r *ClassCacheReconciler) observePhase(ctx context.Context, cc *classcachev1.ClassCache) (string, int32, error) {
	ds := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: primerName(cc), Namespace: cc.Namespace}, ds)
	if err != nil {
		return PhasePending, 0, err
	}
	if ds.Status.NumberReady == 0 {
		return PhasePending, 0, nil
	}

	if cc.Spec.PatchMode == "Owned" {
		dep := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{Name: cc.Spec.WorkloadRef.Name, Namespace: cc.Namespace}, dep)
		if err == nil && hasInjectedSidecar(dep) {
			if dep.Status.ReadyReplicas == dep.Status.Replicas && dep.Status.Replicas > 0 {
				return PhaseReady, ds.Status.NumberReady, nil
			}
			return PhaseWorkloadPatched, ds.Status.NumberReady, nil
		}
	}
	return PhasePrimerReady, ds.Status.NumberReady, nil
}

func hasInjectedSidecar(dep *appsv1.Deployment) bool {
	for _, ic := range dep.Spec.Template.Spec.InitContainers {
		if ic.Name == initContainerName {
			return true
		}
	}
	return false
}

// ArchiveKeyFor returns the authoritative archive key set by the primer.
// Returns "" when the primer hasn't registered yet — callers must treat
// that as "not ready, don't patch the workload".
func ArchiveKeyFor(cc *classcachev1.ClassCache) string {
	return cc.Status.ArchiveKey
}

func (r *ClassCacheReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&classcachev1.ClassCache{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}

func init() { _ = fmt.Sprintf } // silence "imported and not used" if helpers shift
