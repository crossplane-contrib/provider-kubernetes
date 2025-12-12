package azure

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Azure/kubelogin/pkg/token"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

type optionsBuilder interface {
	Build(m map[string]string) (*token.Options, error)
}

type servicePrincipalBuilder struct{}
type workloadIdentityBuilder struct{}

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

	kubeloginCLIFlagServerID = "server-id"
)

// WrapRESTConfig configures the supplied REST config to use OAuth2 bearer
// tokens fetched using the supplied Azure Credentials.
func WrapRESTConfig(_ context.Context, rc *rest.Config, credentials []byte, identityType kconfig.IdentityType, _ ...string) error {
	m, err := parseCredentialMap(credentials)
	if err != nil {
		return err
	}

	if err := validateExecProvider(rc); err != nil {
		return err
	}

	baseOpts, err := kubeloginTokenOptionsFromRESTConfig(rc)
	if err != nil {
		return err
	}
	rc.ExecProvider = nil

	identityBuilder, err := builderFor(identityType)
	if err != nil {
		return err
	}

	idOpts, err := identityBuilder.Build(m)
	if err != nil {
		return err
	}

	// merge base options into type-specific options
	idOpts.ServerID = baseOpts.ServerID

	p, err := token.GetTokenProvider(idOpts)
	if err != nil {
		return errors.Wrap(err, "cannot build Azure token provider")
	}

	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &tokenTransport{Provider: p, Base: rt}
	})

	return nil
}

func (b servicePrincipalBuilder) Build(m map[string]string) (*token.Options, error) {
	opts := &token.Options{
		LoginMethod:  token.ServicePrincipalLogin,
		ClientID:     m[CredentialsKeyClientID],
		ClientSecret: m[CredentialsKeyClientSecret],
		TenantID:     m[CredentialsKeyTenantID],
	}

	if cert := m[CredentialsKeyClientCert]; cert != "" {
		opts.ClientCert = cert
		opts.ClientCertPassword = m[CredentialsKeyClientCertPass]
	}

	return opts, nil
}

func (b workloadIdentityBuilder) Build(m map[string]string) (*token.Options, error) {
	return &token.Options{
		LoginMethod:        token.WorkloadIdentityLogin,
		ClientID:           m[CredentialsKeyClientID],
		TenantID:           m[CredentialsKeyTenantID],
		ServerID:           m[CredentialsKeyServerID],
		FederatedTokenFile: m[CredentialsKeyFederatedTokenFile],
		AuthorityHost:      m[CredentialsKeyAuthorityHost],
	}, nil
}

func builderFor(identityType kconfig.IdentityType) (optionsBuilder, error) {
	switch identityType {
	case kconfig.IdentityTypeAzureServicePrincipalCredentials:
		return servicePrincipalBuilder{}, nil
	case kconfig.IdentityTypeAzureWorkloadIdentityCredentials:
		return workloadIdentityBuilder{}, nil
	default:
		return nil, errors.Errorf("unsupported identity type: %s", identityType)
	}
}

func kubeloginTokenOptionsFromRESTConfig(rc *rest.Config) (*token.Options, error) {
	opts := &token.Options{}

	// opts are filled according to the provided args in the execProvider section of the kubeconfig
	// we are parsing serverID from here
	// add other flags if new login methods are introduced
	fs := pflag.NewFlagSet("kubelogin", pflag.ContinueOnError)
	fs.ParseErrorsWhitelist = pflag.ParseErrorsWhitelist{UnknownFlags: true}
	fs.StringVar(&opts.ServerID, kubeloginCLIFlagServerID, "", "Microsoft Entra (AAD) server application id")
	err := fs.Parse(rc.ExecProvider.Args)
	if err != nil {
		return nil, errors.Wrap(err, "could not parse execProvider arguments in kubeconfig")
	}

	return opts, nil
}

func parseCredentialMap(b []byte) (map[string]string, error) {
	m := make(map[string]string)
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.Wrap(err, "invalid Azure credentials JSON")
	}
	return m, nil
}

func validateExecProvider(rc *rest.Config) error {
	if rc.ExecProvider == nil || len(rc.ExecProvider.Args) == 0 {
		return errors.New("kubeconfig missing execProvider arguments required for Azure authentication")
	}
	return nil
}
