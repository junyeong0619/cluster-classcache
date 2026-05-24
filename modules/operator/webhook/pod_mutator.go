// Package webhook implements the MutatingAdmissionWebhook that patches Pods
// referencing a ClassCache by label (classcache.dev/inject=<cc-name>) when the
// owning ClassCache has spec.patchMode=Webhook.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	classcachev1 "github.com/cluster-classcache/operator/api/v1"
	"github.com/cluster-classcache/operator/controllers"
)

const InjectLabel = "classcache.dev/inject"

type PodMutator struct {
	Client  client.Client
	decoder admission.Decoder
}

func NewPodMutator(c client.Client, d admission.Decoder) *PodMutator {
	return &PodMutator{Client: c, decoder: d}
}

func (m *PodMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}
	if err := m.decoder.Decode(req, pod); err != nil {
		return admission.Errored(400, err)
	}

	ccName, ok := pod.Labels[InjectLabel]
	if !ok || ccName == "" {
		return admission.Allowed("no inject label")
	}

	cc := &classcachev1.ClassCache{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: ccName, Namespace: pod.Namespace}, cc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Allowed(fmt.Sprintf("ClassCache %s/%s not found — skipping", pod.Namespace, ccName))
		}
		return admission.Errored(500, err)
	}
	if cc.Spec.PatchMode != "Webhook" {
		return admission.Allowed("classcache patchMode != Webhook")
	}
	if cc.Status.ArchiveKey == "" {
		// Primer hasn't registered an archive yet. Let the Pod start
		// without injection — better to boot slow than to deadlock on a
		// .jsa file that doesn't exist yet.
		return admission.Allowed("classcache archive not ready yet")
	}

	// Reuse the exact same patch logic the controller uses for owned mode,
	// so behavior is identical regardless of integration mode.
	defaults(cc)
	pt := &corev1.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec:       pod.Spec,
	}
	patch := controllers.PatchSpec{
		ArchiveDir:   cc.Spec.ArchiveDir,
		ArchiveKey:   cc.Status.ArchiveKey,
		Profile:      cc.Spec.Profile,
		ClassCacheNS: cc.Namespace,
		CCName:       cc.Name,
	}
	if !controllers.ApplyPodPatch(pt, patch) {
		return admission.Allowed("already patched")
	}
	pod.ObjectMeta = pt.ObjectMeta
	pod.Spec = pt.Spec

	raw, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(500, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, raw)
}

func defaults(cc *classcachev1.ClassCache) {
	if cc.Spec.ArchiveDir == "" {
		cc.Spec.ArchiveDir = "/var/lib/classcache"
	}
}
