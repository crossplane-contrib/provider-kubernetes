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
	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsSubsystem = "client_cache"

	// cacheEventHit is a lookup served by a cached client.
	cacheEventHit = "hit"
	// cacheEventMiss is a lookup that found no cached client and built one.
	cacheEventMiss = "miss"
	// cacheEventEvict is a cached client dropped to stay within the bound.
	cacheEventEvict = "evict"
)

// ClientCacheMetrics reports on the target cluster client cache of an
// IdentityAwareBuilder. Register it with the metrics registry and hand it to
// the builder with WithClientCacheMetrics. The series carry no per-credential
// or per-ProviderConfig labels; a sustained rate of evictions is the signal
// that the cache bound is too small for the credential sets in use.
type ClientCacheMetrics struct {
	size    prometheus.Gauge
	entries prometheus.Gauge
	events  *prometheus.CounterVec
}

// NewClientCacheMetrics returns collectors for a client cache, named
// <namespace>_client_cache_size, <namespace>_client_cache_entries and
// <namespace>_client_cache_events_total, with every event series initialised
// to zero so that rates can be computed from the first scrape. The namespace
// identifies the provider that embeds the builder, e.g. provider_kubernetes.
func NewClientCacheMetrics(namespace string) *ClientCacheMetrics {
	m := &ClientCacheMetrics{
		size: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: metricsSubsystem,
			Name:      "size",
			Help:      "Bound on the number of cached target cluster clients, one per distinct credential set. 0 when unbounded.",
		}),
		entries: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: metricsSubsystem,
			Name:      "entries",
			Help:      "Number of target cluster clients currently cached.",
		}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: metricsSubsystem,
			Name:      "events_total",
			Help:      "Client cache lookups by outcome: hit (cached client reused), miss (no cached client, one was built), evict (least recently used client dropped to stay within the bound).",
		}, []string{"event"}),
	}
	for _, event := range []string{cacheEventHit, cacheEventMiss, cacheEventEvict} {
		m.events.WithLabelValues(event)
	}
	return m
}

// Describe implements prometheus.Collector.
func (m *ClientCacheMetrics) Describe(ch chan<- *prometheus.Desc) {
	m.size.Describe(ch)
	m.entries.Describe(ch)
	m.events.Describe(ch)
}

// Collect implements prometheus.Collector.
func (m *ClientCacheMetrics) Collect(ch chan<- prometheus.Metric) {
	m.size.Collect(ch)
	m.entries.Collect(ch)
	m.events.Collect(ch)
}

func (m *ClientCacheMetrics) event(event string) {
	m.events.WithLabelValues(event).Inc()
}

// WithClientCacheMetrics makes the builder report its client cache to m.
// Without it the builder reports to collectors that are never registered.
func WithClientCacheMetrics(m *ClientCacheMetrics) BuilderOption {
	return func(b *IdentityAwareBuilder) {
		b.metrics = m
	}
}
