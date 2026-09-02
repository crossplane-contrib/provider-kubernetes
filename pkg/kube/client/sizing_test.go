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
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

// distinctSpecs returns n provider configs that each reference a Secret with
// a kubeconfig of its own, plus the Secrets serving them.
func distinctSpecs(n int) ([]kconfig.ProviderConfigSpec, map[string]map[string][]byte) {
	specs := make([]kconfig.ProviderConfigSpec, 0, n)
	secrets := make(map[string]map[string][]byte, n)
	for i := range n {
		name := fmt.Sprintf("kubeconfig-%d", i)
		secrets[name] = map[string][]byte{"kubeconfig": kubeconfigFor(fmt.Sprintf("https://%d.example.org:6443", i))}
		specs = append(specs, pcSpec(name, ""))
	}
	return specs, secrets
}

// gatedLocalClient serves the Secrets only once clientCacheKeyWorkers reads
// are in flight, so that a sequential derivation never completes.
func gatedLocalClient(secrets map[string]map[string][]byte) client.Client {
	var inFlight atomic.Int32
	released := make(chan struct{})
	serve := secretLocalClient(secrets).(*test.MockClient).MockGet
	return &test.MockClient{
		MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
			if inFlight.Add(1) == clientCacheKeyWorkers {
				close(released)
			}
			select {
			case <-released:
				return serve(ctx, key, obj)
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

func TestClientCacheSizeFor(t *testing.T) {
	type args struct {
		ctx   context.Context
		local client.Client
		pcs   []kconfig.ProviderConfigSpec
	}
	type want struct {
		sizing ClientCacheSizing
		err    bool
	}

	tenSpecs, tenSecrets := distinctSpecs(10)
	workerSpecs, workerSecrets := distinctSpecs(clientCacheKeyWorkers)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	bounded, cancelBounded := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancelBounded)

	cases := map[string]struct {
		args args
		want want
	}{
		"NoProviderConfigsUseTheDefault": {
			args: args{ctx: context.Background(), local: secretLocalClient(nil)},
			want: want{sizing: ClientCacheSizing{Size: DefaultClientCacheSize}},
		},
		"SharedCredentialsCountOnce": {
			args: args{
				ctx:   context.Background(),
				local: secretLocalClient(tenSecrets),
				pcs:   []kconfig.ProviderConfigSpec{tenSpecs[0], tenSpecs[0], tenSpecs[1]},
			},
			want: want{sizing: ClientCacheSizing{CredentialSets: 2, Size: DefaultClientCacheSize}},
		},
		"HeadroomAboveTheDefault": {
			args: args{ctx: context.Background(), local: secretLocalClient(tenSecrets), pcs: tenSpecs},
			want: want{sizing: ClientCacheSizing{CredentialSets: 10, Size: 11}},
		},
		"UnresolvedCountsAsItsOwnSet": {
			args: args{
				ctx:   context.Background(),
				local: secretLocalClient(tenSecrets),
				pcs:   append([]kconfig.ProviderConfigSpec{pcSpec("missing", "")}, tenSpecs...),
			},
			want: want{sizing: ClientCacheSizing{CredentialSets: 10, Unresolved: 1, Size: 13}},
		},
		"KeysAreDerivedConcurrently": {
			args: args{ctx: bounded, local: gatedLocalClient(workerSecrets), pcs: workerSpecs},
			want: want{sizing: ClientCacheSizing{CredentialSets: clientCacheKeyWorkers, Size: 18}},
		},
		"ExpiredContextIsAnError": {
			args: args{
				ctx: cancelled,
				local: &test.MockClient{MockGet: func(ctx context.Context, _ client.ObjectKey, _ client.Object) error {
					return ctx.Err()
				}},
				pcs: tenSpecs,
			},
			want: want{err: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ClientCacheSizeFor(tc.args.ctx, tc.args.local, tc.args.pcs)
			if diff := cmp.Diff(tc.want.err, err != nil); diff != "" {
				t.Fatalf("ClientCacheSizeFor(...): -want error, +got error (%v):\n%s", err, diff)
			}
			if diff := cmp.Diff(tc.want.sizing, got); diff != "" {
				t.Errorf("ClientCacheSizeFor(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestClientCacheSizeForCount(t *testing.T) {
	cases := map[string]struct {
		credentialSets int
		want           int
	}{
		"NoneIsTheDefault":       {credentialSets: 0, want: DefaultClientCacheSize},
		"BelowTheDefault":        {credentialSets: 7, want: DefaultClientCacheSize},
		"HeadroomLeavesDefault":  {credentialSets: 8, want: 9},
		"HeadroomRoundsUp":       {credentialSets: 12, want: 14},
		"HeadroomIsProportional": {credentialSets: 100, want: 110},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, clientCacheSizeFor(tc.credentialSets)); diff != "" {
				t.Errorf("clientCacheSizeFor(%d): -want, +got:\n%s", tc.credentialSets, diff)
			}
		})
	}
}
