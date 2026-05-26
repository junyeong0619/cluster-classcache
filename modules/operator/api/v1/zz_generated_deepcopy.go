// Hand-written DeepCopy implementations (no controller-gen).
// Keep field-by-field in sync with classcache_types.go.

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *WorkloadRef) DeepCopyInto(out *WorkloadRef) { *out = *in }
func (in *AppSpec) DeepCopyInto(out *AppSpec)         { *out = *in /* ExtractorImage is a plain string */ }
func (in *AgentSpec) DeepCopyInto(out *AgentSpec)     { *out = *in }
func (in *ValkeySpec) DeepCopyInto(out *ValkeySpec)   { *out = *in }

func (in *ClassCacheSpec) DeepCopyInto(out *ClassCacheSpec) {
	*out = *in
	in.WorkloadRef.DeepCopyInto(&out.WorkloadRef)
	in.App.DeepCopyInto(&out.App)
	in.Agent.DeepCopyInto(&out.Agent)
	in.Valkey.DeepCopyInto(&out.Valkey)
}

func (in *ClassCacheStatus) DeepCopyInto(out *ClassCacheStatus) {
	*out = *in
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

func (in *ClassCache) DeepCopyInto(out *ClassCache) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *ClassCache) DeepCopy() *ClassCache {
	if in == nil {
		return nil
	}
	out := new(ClassCache)
	in.DeepCopyInto(out)
	return out
}

func (in *ClassCache) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *ClassCacheList) DeepCopyInto(out *ClassCacheList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ClassCache, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *ClassCacheList) DeepCopy() *ClassCacheList {
	if in == nil {
		return nil
	}
	out := new(ClassCacheList)
	in.DeepCopyInto(out)
	return out
}

func (in *ClassCacheList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
