package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

func (in *ServiceAccountToken) DeepCopyInto(out *ServiceAccountToken) {
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec = in.Spec
	out.Spec.ForProvider.Audiences = append([]string(nil), in.Spec.ForProvider.Audiences...)
	if in.Status.AtProvider.ExpirationTimestamp != nil {
		t := *in.Status.AtProvider.ExpirationTimestamp
		out.Status.AtProvider.ExpirationTimestamp = &t
	}
}
func (in *ServiceAccountToken) DeepCopy() *ServiceAccountToken {
	if in == nil {
		return nil
	}
	out := new(ServiceAccountToken)
	in.DeepCopyInto(out)
	return out
}
func (in *ServiceAccountToken) DeepCopyObject() runtime.Object { return in.DeepCopy() }
func (in *ServiceAccountTokenList) DeepCopyInto(out *ServiceAccountTokenList) {
	*out = *in
	out.ListMeta = in.ListMeta
	out.Items = make([]ServiceAccountToken, len(in.Items))
	for i := range in.Items {
		in.Items[i].DeepCopyInto(&out.Items[i])
	}
}
func (in *ServiceAccountTokenList) DeepCopy() *ServiceAccountTokenList {
	if in == nil {
		return nil
	}
	out := new(ServiceAccountTokenList)
	in.DeepCopyInto(out)
	return out
}
func (in *ServiceAccountTokenList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
