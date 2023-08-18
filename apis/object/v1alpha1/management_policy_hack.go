package v1alpha1

import xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"

// Note(turkenh): Provider Kubernetes Object already has a ManagementPolicy
// field and implements the logic in its own controller.
// This file contains temporary hacks until we remove the ManagementPolicy field
// from the Provider Kubernetes Object in favor of the one in the ResourceSpec.
// Ultimately, we should remove the ManagementPolicy field from the Provider
// Kubernetes Object and use the one in the ResourceSpec with the help of
// a conversion webhook.
// Something like https://github.com/crossplane/crossplane/pull/3822#issuecomment-1550039349

// A ResourceSpec defines the desired state of a managed resource.
type ResourceSpec struct {
	// WriteConnectionSecretToReference specifies the namespace and name of a
	// Secret to which any connection details for this managed resource should
	// be written. Connection details frequently include the endpoint, username,
	// and password required to connect to the managed resource.
	// This field is planned to be replaced in a future release in favor of
	// PublishConnectionDetailsTo. Currently, both could be set independently
	// and connection details would be published to both without affecting
	// each other.
	// +optional
	WriteConnectionSecretToReference *xpv1.SecretReference `json:"writeConnectionSecretToRef,omitempty"`

	// PublishConnectionDetailsTo specifies the connection secret config which
	// contains a name, metadata and a reference to secret store config to
	// which any connection details for this managed resource should be written.
	// Connection details frequently include the endpoint, username,
	// and password required to connect to the managed resource.
	// +optional
	PublishConnectionDetailsTo *xpv1.PublishConnectionDetailsTo `json:"publishConnectionDetailsTo,omitempty"`

	// ProviderConfigReference specifies how the provider that will be used to
	// create, observe, update, and delete this managed resource should be
	// configured.
	// +kubebuilder:default={"name": "default"}
	ProviderConfigReference *xpv1.Reference `json:"providerConfigRef,omitempty"`

	// ProviderReference specifies the provider that will be used to create,
	// observe, update, and delete this managed resource.
	// Deprecated: Please use ProviderConfigReference, i.e. `providerConfigRef`
	ProviderReference *xpv1.Reference `json:"providerRef,omitempty"`

	// THIS IS AN ALPHA FIELD. Do not use it in production. It is not honored
	// unless the relevant Crossplane feature flag is enabled, and may be
	// changed or removed without notice.
	// ManagementPolicy specifies the level of control Crossplane has over the
	// managed external resource.
	// This field is planned to replace the DeletionPolicy field in a future
	// release. Currently, both could be set independently and non-default
	// values would be honored if the feature flag is enabled.
	// See the design doc for more information: https://github.com/crossplane/crossplane/blob/499895a25d1a1a0ba1604944ef98ac7a1a71f197/design/design-doc-observe-only-resources.md?plain=1#L223
	// +optional
	// +kubebuilder:default=FullControl
	// ManagementPolicy xpv1.ManagementPolicy `json:"managementPolicy,omitempty"`

	// DeletionPolicy specifies what will happen to the underlying external
	// when this managed resource is deleted - either "Delete" or "Orphan" the
	// external resource.
	// This field is planned to be deprecated in favor of the ManagementPolicy
	// field in a future release. Currently, both could be set independently and
	// non-default values would be honored if the feature flag is enabled.
	// See the design doc for more information: https://github.com/crossplane/crossplane/blob/499895a25d1a1a0ba1604944ef98ac7a1a71f197/design/design-doc-observe-only-resources.md?plain=1#L223
	// +optional
	// +kubebuilder:default=Delete
	DeletionPolicy xpv1.DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// GetManagementPolicies of this Object.
func (mg *Object) GetManagementPolicies() xpv1.ManagementPolicies {
	// Note(turkenh): Crossplane runtime reconciler should leave handling of
	// ManagementPolicies to the provider controller. This is a temporary hack
	// until we remove the ManagementPolicy field from the Provider Kubernetes
	// Object in favor of the one in the ResourceSpec.
	return []xpv1.ManagementAction{xpv1.ManagementActionAll}
}

// SetManagementPolicies of this Object.
func (mg *Object) SetManagementPolicies(r xpv1.ManagementPolicies) {}
