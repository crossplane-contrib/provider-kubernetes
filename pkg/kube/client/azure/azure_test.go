package azure

import (
	"testing"

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
