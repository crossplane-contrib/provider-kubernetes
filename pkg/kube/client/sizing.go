/*
Copyright 2026 The Crossplane Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package client

import (
	"context"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

// clientCacheKeyWorkers bounds the number of provider configs whose keys
// ClientCacheSizeFor derives at once, and with it the credential reads in
// flight against the local cluster.
const clientCacheKeyWorkers = 16

// ClientCacheSizing is the outcome of ClientCacheSizeFor.
type ClientCacheSizing struct {
	// CredentialSets is the number of distinct clients the provider configs
	// resolve to: configs whose credential material digests to the same key
	// share one client.
	CredentialSets int
	// Unresolved is the number of provider configs whose key could not be
	// derived, for example because the Secret they reference is missing.
	// Each is assumed to be a credential set of its own.
	Unresolved int
	// Size is the resulting client cache bound.
	Size int
}

// ClientCacheSizeFor sizes the client cache of an IdentityAwareBuilder for the
// given provider configs so that every one of them keeps its client cached:
// one entry per distinct credential set plus a tenth of headroom, rounded up,
// for sets whose credentials rotate (the client built from the old
// credentials occupies an entry until it ages out), and never below
// DefaultClientCacheSize. Keys are derived by
// a builder over local, exactly as KubeForProviderConfig derives them, so
// configs that share credential material count once; up to
// clientCacheKeyWorkers configs are derived at a time. A config whose key
// cannot be derived is counted as a credential set of its own, so the estimate
// errs high; an expired context is the only error, since a partial derivation
// would not be a bound at all.
func ClientCacheSizeFor(ctx context.Context, local client.Client, pcs []kconfig.ProviderConfigSpec) (ClientCacheSizing, error) {
	b := NewIdentityAwareBuilder(local)
	// An empty key marks a config whose key could not be derived.
	keys := make([]string, len(pcs))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(clientCacheKeyWorkers)
	for i, pc := range pcs {
		g.Go(func() error {
			key, err := b.ClientCacheKey(ctx, pc)
			if err != nil && ctx.Err() != nil {
				return errors.Wrap(err, "cannot derive client cache keys")
			}
			keys[i] = key
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return ClientCacheSizing{}, err
	}

	distinct := make(map[string]struct{}, len(keys))
	s := ClientCacheSizing{}
	for _, key := range keys {
		if key == "" {
			s.Unresolved++
			continue
		}
		distinct[key] = struct{}{}
	}
	s.CredentialSets = len(distinct)
	s.Size = clientCacheSizeFor(s.CredentialSets + s.Unresolved)
	return s, nil
}

// clientCacheSizeFor returns the cache bound for n credential sets: n plus a
// tenth of headroom, rounded up, and never below DefaultClientCacheSize.
func clientCacheSizeFor(n int) int {
	return max(DefaultClientCacheSize, n+(n+9)/10)
}
