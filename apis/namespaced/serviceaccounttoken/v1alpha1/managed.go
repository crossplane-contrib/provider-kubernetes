package v1alpha1

import xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

func (r *ServiceAccountToken) GetCondition(t xpv2.ConditionType) xpv2.Condition {
	return r.Status.GetCondition(t)
}
func (r *ServiceAccountToken) SetConditions(c ...xpv2.Condition) { r.Status.SetConditions(c...) }
func (r *ServiceAccountToken) GetManagementPolicies() xpv2.ManagementPolicies {
	return r.Spec.ManagementPolicies
}
func (r *ServiceAccountToken) SetManagementPolicies(p xpv2.ManagementPolicies) {
	r.Spec.ManagementPolicies = p
}
func (r *ServiceAccountToken) GetProviderConfigReference() *xpv2.ProviderConfigReference {
	return r.Spec.ProviderConfigReference
}
func (r *ServiceAccountToken) SetProviderConfigReference(p *xpv2.ProviderConfigReference) {
	r.Spec.ProviderConfigReference = p
}
func (r *ServiceAccountToken) GetWriteConnectionSecretToReference() *xpv2.LocalSecretReference {
	return r.Spec.WriteConnectionSecretToReference
}
func (r *ServiceAccountToken) SetWriteConnectionSecretToReference(p *xpv2.LocalSecretReference) {
	r.Spec.WriteConnectionSecretToReference = p
}
