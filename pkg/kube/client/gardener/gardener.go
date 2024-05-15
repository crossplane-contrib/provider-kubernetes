package gardener

import (
	"context"
	"sync"
	"time"

	authenticationv1alpha1 "github.com/gardener/gardener/pkg/apis/authentication/v1alpha1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type cache struct {
	shootRef *types.NamespacedName
	config   *rest.Config
	ttl      time.Time
}

var (
	caches []*cache
	mutex  sync.Mutex
)

// GetShootAuthFromCache searches cache for Kubeconfig of the Shoot cluster.
// If none is found, or the lifetime of has expired a new one is requested and stored in cache.
func GetShootAuthFromCache(ctx context.Context, gardenClient client.SubResourceClientConstructor, shootRef *types.NamespacedName) (*rest.Config, error) {
	mutex.Lock()
	var newCaches []*cache //nolint:prealloc
	var err error
	defer func() {
		if err == nil {
			caches = newCaches
		}
		mutex.Unlock()
	}()

	currentTime := time.Now()
	for _, cache := range caches {
		if !currentTime.Before(cache.ttl) {
			// remove it from cache
			continue
		}
		newCaches = append(newCaches, cache)

		if cache.shootRef.Name == shootRef.Name && cache.shootRef.Namespace == shootRef.Namespace {
			return cache.config, nil
		}
	}

	cache := &cache{shootRef: shootRef}
	cache.config, cache.ttl, err = getShootAuth(ctx, gardenClient, shootRef)
	if err != nil {
		return nil, err
	}

	newCaches = append(newCaches, cache)
	return cache.config, nil
}

func getShootAuth(ctx context.Context, gardenClient client.SubResourceClientConstructor, shootRef *types.NamespacedName) (*rest.Config, time.Time, error) {
	expiration := 10 * time.Minute
	expirationSeconds := int64(expiration.Seconds())
	adminKubeconfigRequest := &authenticationv1alpha1.AdminKubeconfigRequest{
		Spec: authenticationv1alpha1.AdminKubeconfigRequestSpec{
			ExpirationSeconds: &expirationSeconds,
		},
	}

	shoot := &gardencorev1beta1.Shoot{
		ObjectMeta: v1.ObjectMeta{Name: shootRef.Name, Namespace: shootRef.Namespace},
	}

	if err := gardenClient.SubResource("adminkubeconfig").Create(ctx, shoot, adminKubeconfigRequest); err != nil {
		return nil, time.Time{}, err
	}

	kubeconfig := adminKubeconfigRequest.Status.Kubeconfig
	shootRESTConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, time.Time{}, err
	}

	expirationTimestamp := adminKubeconfigRequest.Status.ExpirationTimestamp

	return shootRESTConfig, expirationTimestamp.Time, nil
}
