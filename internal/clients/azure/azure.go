package azure

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Azure/kubelogin/pkg/token"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

// Credentials Secret content is a json whose keys are below.
const (
	CredentialsKeyClientID           = "clientId"
	CredentialsKeyClientSecret       = "clientSecret"
	CredentialsKeyTenantID           = "tenantId"
	CredentialsKeyClientCert         = "clientCertificate"
	CredentialsKeyClientCertPass     = "clientCertificatePassword"
	CredentialsKeyFederatedTokenFile = "federatedTokenFile"
	CredentialsKeyAuthorityHost      = "authorityHost"
	CredentialsKeyServerID           = "serverId"
)

// WrapRESTConfig configures the supplied REST config to use OAuth2 bearer
// tokens fetched using the supplied Azure Credentials.
func WrapRESTConfig(_ context.Context, rc *rest.Config, credentials []byte, identityType apisv1alpha1.IdentityType, _ ...string) error {
	m := map[string]string{}
	if err := json.Unmarshal(credentials, &m); err != nil {
		return err
	}

	if rc.ExecProvider == nil || rc.ExecProvider.Args == nil || len(rc.ExecProvider.Args) < 1 {
		return errors.New("an identity configuration was specified but the provided kubeconfig does not have execProvider section")
	}

	fs := pflag.NewFlagSet("kubelogin", pflag.ContinueOnError)
	opts := token.NewOptions()
	opts.AddFlags(fs)
	// opts are filled according to the provided args in the execProvider section of the kubeconfig
	// we are parsing serverID from here
	// this will also parse other flags, that will help future integrations with other auth types
	// see token.Options struct for options reference
	err := fs.Parse(rc.ExecProvider.Args)
	if err != nil {
		return errors.Wrap(err, "could not parse execProvider arguments in kubeconfig")
	}
	rc.ExecProvider = nil
	switch identityType {
	case apisv1alpha1.IdentityTypeAzureServicePrincipalCredentials:
		opts.LoginMethod = token.ServicePrincipalLogin
		opts.ClientID = m[CredentialsKeyClientID]
		opts.ClientSecret = m[CredentialsKeyClientSecret]
		opts.TenantID = m[CredentialsKeyTenantID]
		if cert, ok := m[CredentialsKeyClientCert]; ok {
			opts.ClientCert = cert
			if certpass, ok2 := m[CredentialsKeyClientCertPass]; ok2 {
				opts.ClientCertPassword = certpass
			}
		}
	case apisv1alpha1.IdentityTypeAzureWorkloadIdentityCredentials:
		opts.LoginMethod = token.WorkloadIdentityLogin
		opts.ClientID = m[CredentialsKeyClientID]
		opts.TenantID = m[CredentialsKeyTenantID]
		opts.ServerID = m[CredentialsKeyServerID]
		opts.FederatedTokenFile = m[CredentialsKeyFederatedTokenFile]
		opts.AuthorityHost = m[CredentialsKeyAuthorityHost]
	}

	p, err := token.NewTokenProvider(&opts)
	if err != nil {
		return errors.Wrap(err, "cannot build azure token provider")
	}

	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &tokenTransport{Provider: p, Base: rt}
	})

	return nil
}
