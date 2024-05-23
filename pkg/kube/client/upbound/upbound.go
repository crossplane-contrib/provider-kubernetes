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
)

// WrapRESTConfig configures the supplied REST config to use OAuth2 access
// tokens fetched using the supplied Upbound session/robot token.
func WrapRESTConfig(_ context.Context, rc *rest.Config, token string, store *token.ReuseSourceStore) error { // nolint:gocyclo // mostly error handling
	ex := rc.ExecProvider
	if ex == nil {
		return errors.New("an identity configuration was specified but the provided kubeconfig does not have execProvider section")
	}
	if ex.APIVersion != "client.authentication.k8s.io/v1" {
		return errors.New("execProvider APIVersion is not client.authentication.k8s.io/v1")
	}
	if ex.Command != "up" || len(ex.Args) < 2 || ex.Args[0] != "organization" || ex.Args[1] != "token" {
		return errors.New("execProvider command is not up organization token")
	}

	// Read the org name, it could either be the 3rd argument and provided with `ORGANIZATION` env var.
	org := ""
	if len(ex.Args) > 2 {
		org = ex.Args[2]
	}
	for _, env := range ex.Env {
		if env.Name == "ORGANIZATION" {
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
		client: auth.NewClient(&up.Config{
			Client: up.NewClient(func(client *up.HTTPClient) {
				client.BaseURL.Host = authHost
			}),
		}),
		org:          org,
		refreshToken: token,
	}

	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &oauth2.Transport{Source: store.SourceForRefreshToken(token, us), Base: rt}
	})

	return nil
}

// upboundTokenSource is an oauth2.TokenSource that fetches tokens from Upbound.
type upboundTokenSource struct {
	client       *auth.Client
	org          string
	refreshToken string
	// Base is the base RoundTripper used to make HTTP requests.
	// If nil, http.DefaultTransport is used.
	Base http.RoundTripper
}

func (s *upboundTokenSource) Token() (*oauth2.Token, error) {
	resp, err := s.client.GetOrgScopedToken(context.Background(), s.org, s.refreshToken)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get upbound org scoped token")
	}
	return &oauth2.Token{
		RefreshToken: s.refreshToken,
		AccessToken:  resp.AccessToken,
		Expiry:       time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}, nil
}
