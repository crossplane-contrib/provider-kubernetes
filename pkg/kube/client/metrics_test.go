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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

// cacheReport is what the collectors expose after a sequence of lookups.
type cacheReport struct {
	size    float64
	entries float64
	hits    float64
	misses  float64
	evicts  float64
}

func value(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		t.Fatalf("Write(...): unexpected error: %v", err)
	}
	if out.GetCounter() != nil {
		return out.GetCounter().GetValue()
	}
	return out.GetGauge().GetValue()
}

func report(t *testing.T, m *ClientCacheMetrics) cacheReport {
	t.Helper()
	return cacheReport{
		size:    value(t, m.size),
		entries: value(t, m.entries),
		hits:    value(t, m.events.WithLabelValues(cacheEventHit)),
		misses:  value(t, m.events.WithLabelValues(cacheEventMiss)),
		evicts:  value(t, m.events.WithLabelValues(cacheEventEvict)),
	}
}

func TestClientCacheMetrics(t *testing.T) {
	secrets := map[string]map[string][]byte{
		"kubeconfig-a": {"kubeconfig": kubeconfigFor("https://a.example.org:6443")},
		"kubeconfig-b": {"kubeconfig": kubeconfigFor("https://b.example.org:6443")},
	}

	type args struct {
		cacheSize int
		calls     []kconfig.ProviderConfigSpec
	}
	type want struct {
		report cacheReport
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"FreshBuilderReportsZeroes": {
			args: args{cacheSize: DefaultClientCacheSize},
			want: want{report: cacheReport{size: DefaultClientCacheSize}},
		},
		"RepeatedLookupsHit": {
			args: args{
				cacheSize: DefaultClientCacheSize,
				calls:     []kconfig.ProviderConfigSpec{pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-a", "")},
			},
			want: want{report: cacheReport{size: DefaultClientCacheSize, entries: 1, hits: 2, misses: 1}},
		},
		"DistinctCredentialsMissAndFill": {
			args: args{
				cacheSize: DefaultClientCacheSize,
				calls:     []kconfig.ProviderConfigSpec{pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-b", "")},
			},
			want: want{report: cacheReport{size: DefaultClientCacheSize, entries: 2, misses: 2}},
		},
		"OverflowEvicts": {
			args: args{
				cacheSize: 1,
				calls:     []kconfig.ProviderConfigSpec{pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-b", ""), pcSpec("kubeconfig-a", "")},
			},
			want: want{report: cacheReport{size: 1, entries: 1, misses: 3, evicts: 2}},
		},
		"UnboundedReportsZeroSize": {
			args: args{
				cacheSize: 0,
				calls:     []kconfig.ProviderConfigSpec{pcSpec("kubeconfig-a", ""), pcSpec("kubeconfig-b", "")},
			},
			want: want{report: cacheReport{entries: 2, misses: 2}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := NewClientCacheMetrics("provider_test")
			b := NewIdentityAwareBuilder(secretLocalClient(secrets), WithClientCacheSize(tc.args.cacheSize), WithClientCacheMetrics(m))

			for i, pc := range tc.args.calls {
				if _, _, err := b.KubeForProviderConfig(context.Background(), pc); err != nil {
					t.Fatalf("KubeForProviderConfig(...) call %d: unexpected error: %v", i, err)
				}
			}

			if diff := cmp.Diff(tc.want.report, report(t, m), cmp.AllowUnexported(cacheReport{})); diff != "" {
				t.Errorf("client cache metrics: -want, +got:\n%s", diff)
			}
		})
	}
}

func TestClientCacheMetricsRegister(t *testing.T) {
	cases := map[string]struct {
		metrics *ClientCacheMetrics
	}{
		"CollectorsRegisterOnce": {metrics: NewClientCacheMetrics("provider_kubernetes")},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reg := prometheus.NewPedanticRegistry()
			if err := reg.Register(tc.metrics); err != nil {
				t.Fatalf("Register(...): unexpected error: %v", err)
			}
			families, err := reg.Gather()
			if err != nil {
				t.Fatalf("Gather(): unexpected error: %v", err)
			}
			names := make([]string, 0, len(families))
			for _, f := range families {
				names = append(names, f.GetName())
			}
			want := []string{"provider_kubernetes_client_cache_entries", "provider_kubernetes_client_cache_events_total", "provider_kubernetes_client_cache_size"}
			if diff := cmp.Diff(want, names); diff != "" {
				t.Errorf("Gather(): -want families, +got families:\n%s", diff)
			}
		})
	}
}
