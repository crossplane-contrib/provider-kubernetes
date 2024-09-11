// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package ssa

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/crossplane-contrib/provider-kubernetes/apis/v1alpha1"
)

type testClusterTarget struct {
	gvs []schema.GroupVersion
}

func buildTestCacheWithGVs(gvs []schema.GroupVersion) *GVKParserCache {
	cache := &GVKParserCache{
		store: map[schema.GroupVersion]*GVKParserCacheEntry{},
	}
	for _, gv := range gvs {
		cache.store[gv] = &GVKParserCacheEntry{}
	}
	return cache
}

func buildParserCacheManagerStore(targets map[types.UID]testClusterTarget) map[types.UID]*GVKParserCache {
	store := make(map[types.UID]*GVKParserCache)
	for uid, target := range targets {
		cache := buildTestCacheWithGVs(target.gvs)
		store[uid] = cache
	}
	return store
}

func TestParserCacheManager(t *testing.T) {
	tests := []struct {
		name               string
		testClusterTargets map[types.UID]testClusterTarget
		wantCachedPCs      []*v1alpha1.ProviderConfig
		wantUncachedPCs    []*v1alpha1.ProviderConfig
	}{
		{
			name: "Test Load",
			testClusterTargets: map[types.UID]testClusterTarget{
				types.UID("foo"): {
					gvs: []schema.GroupVersion{
						{Group: "cats.example.com", Version: "v1"},
						{Group: "dogs.example.com", Version: "v1"},
					},
				},
				types.UID("baz"): {
					gvs: []schema.GroupVersion{
						{Group: "trees.example.com", Version: "v1"},
						{Group: "flowers.example.com", Version: "v1"},
					},
				},
			},
			wantCachedPCs: []*v1alpha1.ProviderConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: types.UID("foo"),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: types.UID("baz"),
					},
				},
			},
			wantUncachedPCs: []*v1alpha1.ProviderConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: types.UID("bar"),
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewGVKParserCacheManager()
			manager.cacheStore = buildParserCacheManagerStore(test.testClusterTargets)
			for _, pc := range test.wantUncachedPCs {
				cache, err := manager.LoadOrNewCacheForProviderConfig(pc)
				if err != nil {
					t.Fatalf("cache creation returns err: %v", err)
				}
				if cache == nil {
					t.Fatalf("cache was nil for uid %v", pc)
				}
				if len(cache.store) != 0 {
					t.Fatalf("expected empty cache, got cache with GVs %v", cache)
				}
			}

			for _, pc := range test.wantCachedPCs {
				cache, err := manager.LoadOrNewCacheForProviderConfig(pc)
				if err != nil {
					t.Fatalf("cache retrieval for providerconfig returns err: %v", err)
				}
				if cache == nil {
					t.Fatalf("cache was nil for uid %v", pc)
				}
				if diff := cmp.Diff(len(cache.store), len(test.testClusterTargets[pc.GetUID()].gvs)); diff != "" {
					t.Fatalf("cache length: -want length, +got length\n: %v", diff)
				}
				var cacheKeys []schema.GroupVersion
				for k := range cache.store {
					cacheKeys = append(cacheKeys, k)
				}
				less := func(a, b schema.GroupVersion) bool { return a.String() < b.String() }
				if diff := cmp.Diff(test.testClusterTargets[pc.GetUID()].gvs, cacheKeys, cmpopts.SortSlices(less)); diff != "" {
					t.Fatalf("cache content: -want, +got\n: %v", diff)
				}
			}

			if diff := cmp.Diff(len(test.wantCachedPCs)+len(test.wantUncachedPCs), len(manager.cacheStore)); diff != "" {
				t.Fatalf("managed cache count: -want, +got\n: %v", diff)
			}

			for _, pc := range test.wantCachedPCs {
				manager.RemoveCache(pc)
				cache, err := manager.LoadOrNewCacheForProviderConfig(pc)
				if err != nil {
					t.Fatalf("cache creation returns err: %v", err)
				}
				if cache == nil {
					t.Fatalf("cache was nil for PC %v", pc)
				}
				if len(cache.store) != 0 {
					t.Fatalf("expected empty cache, got cache with GVs %v", cache)
				}
			}

		})
	}
}
