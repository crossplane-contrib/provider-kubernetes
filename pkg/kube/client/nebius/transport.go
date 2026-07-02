package nebius

import (
	"context"
	"net/http"

	"github.com/nebius/gosdk/auth"
	"github.com/pkg/errors"
)

// tokenSource yields a Nebius IAM bearer token. Satisfied by *gosdk.SDK and
// faked in tests.
type tokenSource interface {
	BearerToken(ctx context.Context) (auth.BearerToken, error)
}

// tokenTransport is an http.RoundTripper that injects a Nebius IAM token into
// requests, wrapping a base RoundTripper and adding an Authorization header.
type tokenTransport struct {
	Source tokenSource
	// Base is the base RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
}

// RoundTrip authenticates the request with a bearer token from the Source.
func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBodyClosed := false
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				req.Body.Close() //nolint:errcheck
			}
		}()
	}

	tkn, err := t.Source.BearerToken(req.Context())
	if err != nil {
		return nil, errors.Wrap(err, "cannot get Nebius IAM token")
	}
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+tkn.Token)

	// req.Body is assumed to be closed by the base RoundTripper.
	reqBodyClosed = true
	return t.base().RoundTrip(req2)
}

func (t *tokenTransport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}
