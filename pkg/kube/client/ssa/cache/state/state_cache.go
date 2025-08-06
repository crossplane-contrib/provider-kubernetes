package state

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	xpresource "github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	objectv1alpha2cluster "github.com/crossplane-contrib/provider-kubernetes/apis/cluster/object/v1alpha2"
	objectv1alpha1namespaced "github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/object/v1alpha1"
)

// CacheManager lets you manage Cache entries for XP managed
// resources
type CacheManager interface {
	LoadOrNewForManaged(mg xpresource.Managed) Cache
	Remove(mg xpresource.Managed)
}

// Cache is the interface for the caching a k8s
// *unstructured.Unstructured object
type Cache interface {
	SetStateFor(obj xpresource.Managed, state *unstructured.Unstructured)
	GetStateFor(obj xpresource.Managed) (*unstructured.Unstructured, bool)
}

// DesiredStateCache is a concurrency-safe implementation of Cache
// that holds a cached k8s object state with a hash key of the associated
// manifest.
// Hash key can be used to determine the validity of the cache entry
type DesiredStateCache struct {
	mu        sync.RWMutex
	extracted *unstructured.Unstructured
	hash      string
}

// GetStateFor returns the stored desired state if exists and valid, for the given *v1alpha2.Object
func (dc *DesiredStateCache) GetStateFor(obj xpresource.Managed) (*unstructured.Unstructured, bool) {
	var manifestSum [32]byte
	switch obj := obj.(type) {
	case *objectv1alpha2cluster.Object:
		manifestSum = sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	case *objectv1alpha1namespaced.Object:
		manifestSum = sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	default:
		return nil, false
	}
	manifestHash := hex.EncodeToString(manifestSum[:])
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if dc.extracted != nil && dc.hash == manifestHash {
		return dc.extracted, true
	}
	return nil, false
}

// SetStateFor stores the desired k8s object state for the given *v1alpha2.Object
func (dc *DesiredStateCache) SetStateFor(obj xpresource.Managed, state *unstructured.Unstructured) {
	var manifestSum [32]byte
	switch obj := obj.(type) {
	case *objectv1alpha2cluster.Object:
		manifestSum = sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	case *objectv1alpha1namespaced.Object:
		manifestSum = sha256.Sum256(obj.Spec.ForProvider.Manifest.Raw)
	default:
		return
	}
	manifestHash := hex.EncodeToString(manifestSum[:])
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.extracted = state
	dc.hash = manifestHash
}

// DesiredStateCacheManager stores the DesiredStateCache instances associated with the
// managed resource instance.
type DesiredStateCacheManager struct {
	mu    sync.RWMutex
	store map[types.UID]Cache
}

// NewDesiredStateCacheManager returns a new DesiredStateCacheManager instance
func NewDesiredStateCacheManager() *DesiredStateCacheManager {
	return &DesiredStateCacheManager{
		store: map[types.UID]Cache{},
	}
}

// LoadOrNewForManaged returns the associated *DesiredStateCache stored in this
// DesiredStateCacheManager for the given managed resource.
// If there is no DesiredStateCache stored previously, a new DesiredStateCache is created and
// stored for the specified managed resource. Subsequent calls with the same managed
// resource will return the previously instantiated and stored DesiredStateCache
// for that managed resource
func (dcs *DesiredStateCacheManager) LoadOrNewForManaged(mg xpresource.Managed) Cache {
	dcs.mu.RLock()
	stateCache, ok := dcs.store[mg.GetUID()]
	dcs.mu.RUnlock()
	if ok {
		return stateCache
	}

	dcs.mu.Lock()
	defer dcs.mu.Unlock()
	// need to recheck cache as might have been populated already
	stateCache, ok = dcs.store[mg.GetUID()]
	if !ok {
		dcs.store[mg.GetUID()] = &DesiredStateCache{}
		stateCache = dcs.store[mg.GetUID()]
	}
	return stateCache
}

// Remove will remove the stored DesiredStateCache of the given managed
// resource from this DesiredStateCacheManager.
func (dcs *DesiredStateCacheManager) Remove(mg xpresource.Managed) {
	dcs.mu.Lock()
	defer dcs.mu.Unlock()
	delete(dcs.store, mg.GetUID())
}
