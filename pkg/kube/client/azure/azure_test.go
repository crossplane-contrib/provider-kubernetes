package azure

import (
	"testing"

	"github.com/Azure/kubelogin/pkg/token"
	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func Test_kubeloginTokenOptionsFromRESTConfig(t *testing.T) {
	t.Run("empty args", func(t *testing.T) {
		rc := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Args: []string{},
			},
		}
		opts, err := kubeloginTokenOptionsFromRESTConfig(rc)
		if err != nil {
			t.Errorf("unexpected error: %s", err)
		}
		if opts == nil {
			t.Errorf("should not be nil")
		} else if opts.ServerID != "" {
			t.Errorf("unexpected server id: %q", opts.ServerID)
		}
	})

	t.Run("parsing server-id", func(t *testing.T) {
		serverID := "foo"
		rc := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Args: []string{"--server-id", serverID},
			},
		}
		opts, err := kubeloginTokenOptionsFromRESTConfig(rc)
		if err != nil {
			t.Errorf("unexpected error: %s", err)
		}
		if opts == nil {
			t.Errorf("should not be nil")
		} else if opts.ServerID != serverID {
			t.Errorf("unexpected server id: %q, expected: %q", opts.ServerID, serverID)
		}
	})
}

func Test_validateExecProvider(t *testing.T) {
	t.Run("nil exec provider", func(t *testing.T) {
		rc := &rest.Config{}
		err := validateExecProvider(rc)
		if err == nil {
			t.Errorf("expected error, got nil")
		}
	})

	t.Run("nil args", func(t *testing.T) {
		rc := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{},
		}
		err := validateExecProvider(rc)
		if err == nil {
			t.Errorf("expected error, got nil")
		}
	})

	t.Run("empty args", func(t *testing.T) {
		rc := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Args: []string{},
			},
		}
		err := validateExecProvider(rc)
		if err == nil {
			t.Errorf("expected error, got nil")
		}
	})

	t.Run("valid exec provider", func(t *testing.T) {
		rc := &rest.Config{
			ExecProvider: &clientcmdapi.ExecConfig{
				Args: []string{"--server-id", "foo"},
			},
		}
		err := validateExecProvider(rc)
		if err != nil {
			t.Errorf("unexpected error: %s", err)
		}
	})
}

func Test_builderFor(t *testing.T) {
	t.Run("service principal", func(t *testing.T) {
		b, err := builderFor(kconfig.IdentityTypeAzureServicePrincipalCredentials)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if _, ok := b.(servicePrincipalBuilder); !ok {
			t.Errorf("expected servicePrincipalBuilder")
		}
	})

	t.Run("workload identity", func(t *testing.T) {
		b, err := builderFor(kconfig.IdentityTypeAzureWorkloadIdentityCredentials)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if _, ok := b.(workloadIdentityBuilder); !ok {
			t.Errorf("expected workloadIdentityBuilder")
		}
	})

	t.Run("unsupported type", func(t *testing.T) {
		_, err := builderFor("nope")
		if err == nil {
			t.Errorf("expected error, got nil")
		}
	})
}

func Test_servicePrincipalBuilder_Build(t *testing.T) {
	b := servicePrincipalBuilder{}

	t.Run("basic SPN fields", func(t *testing.T) {
		m := map[string]string{
			CredentialsKeyClientID:     "cid",
			CredentialsKeyClientSecret: "csec",
			CredentialsKeyTenantID:     "tenant",
		}

		opts, err := b.Build(m)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}

		if opts.LoginMethod != token.ServicePrincipalLogin {
			t.Errorf("unexpected login method: %v", opts.LoginMethod)
		}
		if opts.ClientID != "cid" {
			t.Errorf("expected cid, got %s", opts.ClientID)
		}
		if opts.ClientSecret != "csec" {
			t.Errorf("expected csec, got %s", opts.ClientSecret)
		}
		if opts.TenantID != "tenant" {
			t.Errorf("expected tenant, got %s", opts.TenantID)
		}
		if opts.ClientCert != "" {
			t.Error("expected empty ClientCert")
		}
		if opts.ClientCertPassword != "" {
			t.Error("expected empty ClientCertPassword")
		}
	})
}

func Test_workloadIdentityBuilder_Build(t *testing.T) {
	b := workloadIdentityBuilder{}

	t.Run("basic WI fields", func(t *testing.T) {
		m := map[string]string{
			CredentialsKeyClientID:           "cid",
			CredentialsKeyTenantID:           "tenant",
			CredentialsKeyServerID:           "server",
			CredentialsKeyFederatedTokenFile: "/path/token",
			CredentialsKeyAuthorityHost:      "https://login.microsoftonline.com",
		}

		opts, err := b.Build(m)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}

		if opts.LoginMethod != token.WorkloadIdentityLogin {
			t.Errorf("unexpected login method: %v", opts.LoginMethod)
		}
		if opts.ClientID != "cid" {
			t.Errorf("expected cid, got %s", opts.ClientID)
		}
		if opts.TenantID != "tenant" {
			t.Errorf("expected tenant, got %s", opts.TenantID)
		}
		if opts.ServerID != "server" {
			t.Errorf("expected server, got %s", opts.ServerID)
		}
		if opts.FederatedTokenFile != "/path/token" {
			t.Errorf("expected /path/token, got %s", opts.FederatedTokenFile)
		}
		if opts.AuthorityHost != "https://login.microsoftonline.com" {
			t.Errorf("unexpected authority host: %s", opts.AuthorityHost)
		}
	})
}
