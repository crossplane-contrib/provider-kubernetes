// Package nebius provides in-process authentication to Nebius Managed
// Kubernetes (mk8s) clusters using a service account's authorized key.
package nebius

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nebius/gosdk"
	"github.com/nebius/gosdk/auth"
	"github.com/pkg/errors"
	"k8s.io/client-go/rest"
)

// credentialsFile mirrors the Nebius service account credentials JSON, matching
// the format produced by provider-nebius.
type credentialsFile struct {
	PrivateKey       string `json:"private_key"`
	PublicKeyID      string `json:"public_key_id"`
	ServiceAccountID string `json:"account_id"`
}

// parseCredentials unmarshals and validates the Nebius service account
// credentials JSON held in the identity Secret.
func parseCredentials(credentials []byte) (credentialsFile, error) {
	var cf credentialsFile
	if err := json.Unmarshal(credentials, &cf); err != nil {
		return credentialsFile{}, errors.Wrap(err, "cannot parse Nebius service account credentials")
	}
	if cf.PrivateKey == "" || cf.PublicKeyID == "" || cf.ServiceAccountID == "" {
		return credentialsFile{}, errors.New("Nebius service account credentials must contain non-empty private_key, public_key_id and account_id")
	}
	return cf, nil
}

// newTokenSource builds a gosdk-backed token source. It is a package variable
// so tests can substitute a fake without network access.
var newTokenSource = func(ctx context.Context, privateKey []byte, publicKeyID, serviceAccountID string) (tokenSource, error) {
	sdk, err := gosdk.New(ctx,
		gosdk.WithCredentials(
			gosdk.ServiceAccountReader(
				auth.NewPrivateKeyParser(privateKey, publicKeyID, serviceAccountID),
			),
		),
	)
	if err != nil {
		return nil, err
	}
	return sdk, nil
}

// WrapRESTConfig configures rc to authenticate to a Nebius mk8s cluster using
// IAM tokens minted in-process from the supplied service account credentials.
// A non-nil store reuses the underlying token source across calls so the
// background-refreshing gosdk client is not recreated (and leaked) per reconcile.
func WrapRESTConfig(ctx context.Context, rc *rest.Config, credentials []byte, store *SDKStore) error {
	sc, err := parseCredentials(credentials)
	if err != nil {
		return err
	}

	build := func(ctx context.Context) (tokenSource, error) {
		return newTokenSource(ctx, []byte(sc.PrivateKey), sc.PublicKeyID, sc.ServiceAccountID)
	}

	var src tokenSource
	if store != nil {
		src, err = store.SourceForCredentials(ctx, credentials, build)
	} else {
		src, err = build(ctx)
	}
	if err != nil {
		return errors.Wrap(err, "cannot build Nebius IAM token source")
	}

	rc.ExecProvider = nil
	rc.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &tokenTransport{Source: src, Base: rt}
	})
	return nil
}
