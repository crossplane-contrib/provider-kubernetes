package azure

import (
	"testing"

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
		}
		if opts.ServerID != "" {
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
		}
		if opts.ServerID != serverID {
			t.Errorf("unexpected server id: %q, expected: %q", opts.ServerID, serverID)
		}
	})
}
