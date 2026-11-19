package serviceaccounttoken

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apis "github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/serviceaccounttoken/v1alpha1"
)

func TestValidate(t *testing.T) {
	tests := map[string]struct {
		resource *apis.ServiceAccountToken
		wantErr  bool
	}{
		"valid": {
			resource: &apis.ServiceAccountToken{Spec: apis.ServiceAccountTokenSpec{ForProvider: apis.ServiceAccountTokenParameters{
				ServiceAccountName: "namespace-manager",
				Audiences:          []string{"https://kubernetes.default.svc"},
				ExpirationSeconds:  3600,
			}}},
		},
		"missingServiceAccount": {
			resource: &apis.ServiceAccountToken{Spec: apis.ServiceAccountTokenSpec{ForProvider: apis.ServiceAccountTokenParameters{
				Audiences:         []string{"audience"},
				ExpirationSeconds: 3600,
			}}},
			wantErr: true,
		},
		"missingAudience": {
			resource: &apis.ServiceAccountToken{Spec: apis.ServiceAccountTokenSpec{ForProvider: apis.ServiceAccountTokenParameters{
				ServiceAccountName: "namespace-manager",
				ExpirationSeconds:  3600,
			}}},
			wantErr: true,
		},
		"expirationTooLong": {
			resource: &apis.ServiceAccountToken{Spec: apis.ServiceAccountTokenSpec{ForProvider: apis.ServiceAccountTokenParameters{
				ServiceAccountName: "namespace-manager",
				Audiences:          []string{"audience"},
				ExpirationSeconds:  maxExpirationSeconds + 1,
			}}},
			wantErr: true,
		},
		"negativeExpiration": {
			resource: &apis.ServiceAccountToken{ObjectMeta: metav1.ObjectMeta{Name: "token"}, Spec: apis.ServiceAccountTokenSpec{ForProvider: apis.ServiceAccountTokenParameters{
				ServiceAccountName: "namespace-manager",
				Audiences:          []string{"audience"},
				ExpirationSeconds:  -1,
			}}},
			wantErr: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := validate(tt.resource)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
