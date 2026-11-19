package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

type ServiceAccountTokenParameters struct {
	ServiceAccountName string   `json:"serviceAccountName"`
	Audiences          []string `json:"audiences"`
	ExpirationSeconds  int64    `json:"expirationSeconds"`
}

type ServiceAccountTokenObservation struct {
	ExpirationTimestamp *metav1.Time `json:"expirationTimestamp,omitempty"`
}

type ServiceAccountTokenSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              ServiceAccountTokenParameters `json:"forProvider"`
}

type ServiceAccountTokenStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 ServiceAccountTokenObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,kubernetes}
// +kubebuilder:subresource:status
type ServiceAccountToken struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ServiceAccountTokenSpec   `json:"spec"`
	Status            ServiceAccountTokenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ServiceAccountTokenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceAccountToken `json:"items"`
}

var _ runtime.Object = &ServiceAccountToken{}
