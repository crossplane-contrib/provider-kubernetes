/*
Copyright 2024 The Crossplane Authors.

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

// Package config contains API types used by Crossplane providers interacting
// with Kubernetes APIs.
// +kubebuilder:object:generate=true
package config

import xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

// IdentityType used to authenticate to the Kubernetes API.
// +kubebuilder:validation:Enum=GoogleApplicationCredentials;AzureServicePrincipalCredentials;AzureWorkloadIdentityCredentials;UpboundTokens;AWSWebIdentityCredentials;NebiusServiceAccountCredentials;ServiceAccount
type IdentityType string

// Supported identity types.
const (
	IdentityTypeGoogleApplicationCredentials = "GoogleApplicationCredentials"

	IdentityTypeAzureServicePrincipalCredentials = "AzureServicePrincipalCredentials"

	IdentityTypeAzureWorkloadIdentityCredentials = "AzureWorkloadIdentityCredentials"

	IdentityTypeUpboundTokens = "UpboundTokens"

	IdentityTypeAWSWebIdentityCredentials = "AWSWebIdentityCredentials"

	IdentityTypeNebiusServiceAccountCredentials = "NebiusServiceAccountCredentials"

	IdentityTypeServiceAccount = "ServiceAccount"
)

// ProviderCredentials required to authenticate.
type ProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=None;Secret;InjectedIdentity;Environment;Filesystem
	Source xpv2.CredentialsSource `json:"source"`

	xpv2.CommonCredentialSelectors `json:",inline"`
}

// ServiceAccountRef configures tokens minted via the local cluster's
// TokenRequest API for a ServiceAccount.
type ServiceAccountRef struct {
	// Name of the ServiceAccount to request tokens for.
	Name string `json:"name"`
	// Namespace of the ServiceAccount to request tokens for.
	Namespace string `json:"namespace"`
	// Audiences the minted token is valid for. If empty, the API server's
	// default audience (its own identifier) is used.
	// +optional
	Audiences []string `json:"audiences,omitempty"`
	// ExpirationSeconds is the requested validity duration of the token. The
	// API server may return a token with a longer or shorter lifetime and
	// clamps values outside its configured bounds. Defaults to 3600 (1h)
	// when unset.
	// +optional
	// +kubebuilder:validation:Minimum=600
	ExpirationSeconds *int64 `json:"expirationSeconds,omitempty"`
}

// Identity used to authenticate.
type Identity struct {
	// Type of identity.
	Type IdentityType `json:"type"`

	ProviderCredentials `json:",inline"`

	// ServiceAccountRef configures the ServiceAccount identity type. Required
	// when type is ServiceAccount, ignored otherwise.
	// +optional
	ServiceAccountRef *ServiceAccountRef `json:"serviceAccountRef,omitempty"`
}

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
