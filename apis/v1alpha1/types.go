/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// A ProviderConfigSpec defines the desired state of a ProviderConfig.
type ProviderConfigSpec struct {
	// Credentials used to connect to the Kubernetes API. Typically a
	// kubeconfig file. Use InjectedIdentity for in-cluster config.
	Credentials ProviderCredentials `json:"credentials"`
	// Identity used to authenticate to the Kubernetes API. The identity
	// credentials can be used to supplement kubeconfig 'credentials', for
	// example by configuring a bearer token source such as OAuth.
	// +optional
	Identity *Identity `json:"identity,omitempty"`
}

// ProviderCredentials required to authenticate.
type ProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=None;Secret;InjectedIdentity;Environment;Filesystem
	Source xpv1.CredentialsSource `json:"source"`

	xpv1.CommonCredentialSelectors `json:",inline"`
}

// IdentityType used to authenticate to the Kubernetes API.
type IdentityType string

// Supported identity types.
const (
	IdentityTypeGoogleApplicationCredentials = "GoogleApplicationCredentials"

	IdentityTypeAzureServicePrincipalCredentials = "AzureServicePrincipalCredentials"
)

// Identity used to authenticate.
type Identity struct {
	// Type of identity.
	// +kubebuilder:validation:Enum=GoogleApplicationCredentials;AzureServicePrincipalCredentials
	Type IdentityType `json:"type"`

	ProviderCredentials `json:",inline"`
}

// A ProviderConfigStatus reflects the observed state of a ProviderConfig.
type ProviderConfigStatus struct {
	xpv1.ProviderConfigStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A ProviderConfig configures a Template provider.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="SECRET-NAME",type="string",JSONPath=".spec.credentials.secretRef.name",priority=1
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,kubernetes}
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ProviderConfigList contains a list of ProviderConfig.
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

// +kubebuilder:object:root=true

// A ProviderConfigUsage indicates that a resource is using a ProviderConfig.
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="CONFIG-NAME",type="string",JSONPath=".providerConfigRef.name"
// +kubebuilder:printcolumn:name="RESOURCE-KIND",type="string",JSONPath=".resourceRef.kind"
// +kubebuilder:printcolumn:name="RESOURCE-NAME",type="string",JSONPath=".resourceRef.name"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,kubernetes}
type ProviderConfigUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	xpv1.ProviderConfigUsage `json:",inline"`
}

// +kubebuilder:object:root=true

// ProviderConfigUsageList contains a list of ProviderConfigUsage
type ProviderConfigUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfigUsage `json:"items"`
}

// +kubebuilder:object:root=true

// A ObservedObjectCollection is a provider Kubernetes API type
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="KIND",type="string",JSONPath=".spec.kind"
// +kubebuilder:printcolumn:name="APIVERSION",type="string",JSONPath=".spec.apiVersion",priority=1
// +kubebuilder:printcolumn:name="PROVIDERCONFIG",type="string",JSONPath=".spec.providerConfigRef.name"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,kubernetes}
type ObservedObjectCollection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ObservedObjectCollectionSpec   `json:"spec"`
	Status            ObservedObjectCollectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObservedObjectCollectionList contains a list of ObservedObjectCollection
type ObservedObjectCollectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservedObjectCollection `json:"items"`
}

// ObservedObjectCollectionSpec defines the desired state of ObservedObjectCollection
type ObservedObjectCollectionSpec struct {

	// APIVersion of objects that should be matched by the selector
	// +kubebuilder:validation:MinLength:=1
	APIVersion string `json:"apiVersion"`

	// Kind of objects that should be matched by the selector
	// +kubebuilder:validation:MinLength:=1
	Kind string `json:"kind"`

	// Namespace where to look for objects.
	// If omitted, search is performed across all namespaces.
	// For cluster-scoped objects, omit it.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Selector defines the criteria for including objects into the collection
	Selector metav1.LabelSelector `json:"selector"`

	// ProviderConfigReference specifies how the provider that will be used to
	// create, observe, update, and delete this managed resource should be
	// configured.
	// +kubebuilder:default={"name": "default"}
	ProviderConfigReference xpv1.Reference `json:"providerConfigRef,omitempty"`

	// Template when defined is used for creating Object instances
	// +optional
	Template *ObservedObjectTemplate `json:"template,omitempty"`
}

// ObservedObjectTemplate represents template used when creating observe-only Objects matching the given selector
type ObservedObjectTemplate struct {

	// Objects metadata
	Metadata ObservedObjectTemplateMetadata `json:"metadata,omitempty"`
}

// ObservedObjectTemplateMetadata represents objects metadata
type ObservedObjectTemplateMetadata struct {

	// Labels of an object
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations of an object
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ObservedObjectCollectionStatus represents the observed state of a ObservedObjectCollection
type ObservedObjectCollectionStatus struct {
	xpv1.ResourceStatus `json:",inline"`

	// List of object references mathing the given selector
	// +optional
	// +listType=map
	// +listMapKey=name
	Objects []ObservedObjectReference `json:"objects,omitempty"`
}

// ObservedObjectReference represents a reference to Object with ObserveOnly management policy
type ObservedObjectReference struct {

	// Name of the observed object
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=253
	Name string `json:"name"`
}
