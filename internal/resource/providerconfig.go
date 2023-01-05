package resource

import (
	"context"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

const (
	errExtractServiceAccount = "cannot extract from service account when none specified"
	errGetServiceAccount     = "cannot get service account"
	errTokenRequest          = "cannot create token request"
)

// ExtractServiceAccount extracts credentials from a Kubernetes service account.
func ExtractServiceAccount(ctx context.Context, client client.Client, s apisv1alpha1.KubernetesCredentialSelectors) ([]byte, error) {
	if s.ServiceAccountRef == nil {
		return nil, errors.New(errExtractServiceAccount)
	}
	sa := &corev1.ServiceAccount{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: s.ServiceAccountRef.Namespace, Name: s.ServiceAccountRef.Name}, sa); err != nil {
		return nil, errors.Wrap(err, errGetServiceAccount)
	}
	// Create a TokenRequest for the service account.
	// The name must be the same as the service account name + "-token-<random suffix>".
	tr := &authenticationv1.TokenRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sa.Namespace,
			Name:      sa.Name + "-token-" + uuid.New().String()[0:5],
		},
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{"https://kubernetes.default.svc"},
			ExpirationSeconds: pointer.Int64Ptr(3600), // 1 hour. TODO: make this configurable.
		},
	}
	if err := client.Create(ctx, tr); err != nil {
		return nil, errors.Wrap(err, errTokenRequest)
	}
	// Return the token.
	return []byte(tr.Status.Token), nil
}
