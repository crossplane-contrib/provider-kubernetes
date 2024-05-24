package upbound

import (
	"context"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"github.com/upbound/up-sdk-go"
	"github.com/upbound/up-sdk-go/service/auth"
	"golang.org/x/oauth2"
	"k8s.io/client-go/rest"

	"github.com/crossplane-contrib/provider-kubernetes/pkg/kube/client/token"
)

const (
	// TODO: We may want to make is configurable.
	authHost = "auth.upbound.io"

	envVarOrganization = "ORGANIZATION"
)

// WrapRESTConfig configures the supplied REST config to use OAuth2 access
// tokens fetched using the supplied Upbound session/robot token.
func WrapRESTConfig(ctx context.Context, rc *rest.Config, token string, store *token.ReuseSourceStore) error { // nolint:gocyclo // mostly error handling
	ex := rc.ExecProvider
	if ex == nil {
		return errors.New("an identity configuration was specified but the provided kubeconfig does not have execProvider section")
	}
	if ex.APIVersion != "client.authentication.k8s.io/v1" {
		return errors.New("execProvider APIVersion is not client.authentication.k8s.io/v1")
	}
	if ex.Command != "up" || len(ex.Args) < 2 || (ex.Args[0] != "org" && ex.Args[0] != "organization") || ex.Args[1] != "token" {
		return errors.New("execProvider command is not up organization (org) token")
	}

	// Read the org name, it could either be the 3rd argument or provided with `ORGANIZATION` env var.
	org := ""
	if len(ex.Args) > 2 {
		org = ex.Args[2]
	}
	for _, env := range ex.Env {
		if env.Name == envVarOrganization {
			// Env var takes precedence over the 3rd argument if both are provided.
			org = env.Value
			break
		}
	}
	if org == "" {
		return errors.New("organization name not provided in execProvider args or ORGANIZATION env var")
	}

	rc.ExecProvider = nil

	// DefaultTokenSource retrieves a token source from an injected identity.
	us := &upboundTokenSource{
		ctx: ctx,
		client: auth.NewClient(&up.Config{
			Client: up.NewClient(func(client *up.HTTPClient) {
				client.BaseURL.Host = authHost
			}),
		}),
		org:         org,
		staticToken: token,
	}

	// Mutate the received REST config, rc, to use the Upbound token source at
	// the transport layer.
	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &oauth2.Transport{Source: store.SourceForRefreshToken(token, us), Base: rt}
	})

	return nil
}

// upboundTokenSource is an oauth2.TokenSource that fetches tokens from Upbound.
type upboundTokenSource struct {
	ctx         context.Context
	client      *auth.Client
	org         string
	staticToken string
}

func (s *upboundTokenSource) Token() (*oauth2.Token, error) {
	resp, err := s.client.GetOrgScopedToken(s.ctx, s.org, s.staticToken)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get upbound org scoped token")
	}
	return &oauth2.Token{
		AccessToken: resp.AccessToken,
		Expiry:      time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}, nil
}
