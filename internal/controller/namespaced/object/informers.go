/*
Copyright 2024 The Crossplane Authors.

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

package object

import (
	"context"
	"io"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	kcache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/object/v1alpha1"
)

// resourceInformers manages resource informers referenced or managed
// by Objects. It serves as an event source for realtime notifications of
// changed resources, with the Object reconcilers as sinks.
// It keeps resource informers alive as long as there are Objects referencing
// them. In parallel, the Object reconcilers keep track of references to
// resources, and inform resourceInformers about them via the
// WatchReferencedResources method.
type resourceInformers struct {
	log          logging.Logger
	config       *rest.Config
	objectsCache cache.Cache
	sink         func(providerConfig string, ev runtimeevent.GenericEvent)
	handler      handler.EventHandler
	ps           []predicate.Predicate

	lock sync.RWMutex // everything below is protected by this lock
	// resourceCaches holds the resource caches. These are dynamically started
	// and stopped based on the Objects that reference or managing them.
	resourceCaches map[gvkWithConfig]resourceCache
}

type gvkWithConfig struct {
	// Which provider config was used to create this cache. We will use this
	// information to figure out whether there are Objects relying on this cache
	// left during garbage collection of caches.
	providerConfig string
	gvk            schema.GroupVersionKind
}

type resourceCache struct {
	cache    cache.Cache
	cancelFn context.CancelFunc
	// credentialsKey identifies the credentials the cache was built with.
	// WatchResources replaces the cache when it is called with another key.
	credentialsKey string
}

var _ source.Source = &resourceInformers{}

// Start implements source.Source, i.e. starting resourceInformers as
// source with h as the sink of update events. It keeps sending events until
// ctx is done.
func (i *resourceInformers) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	if i.sink != nil {
		return errors.New("source already started, cannot start it again")
	}
	i.sink = func(providerConfig string, ev runtimeevent.GenericEvent) {
		for _, p := range i.ps {
			if !p.Generic(ev) {
				return
			}
		}
		i.handler.Generic(context.WithValue(ctx, keyProviderConfigName, providerConfig), ev, q)
	}

	go func() {
		<-ctx.Done()
		i.sink = nil
	}()

	return nil
}

// WatchResources starts informers for the given resource GVKs for the given
// cluster (i.e. rest.Config & providerConfig). credentialsKey identifies the
// credentials rc was built with: an informer that was started for the same
// provider config and GVK with other credentials is stopped and started again
// with rc, because its own credentials may have expired (e.g. a kubeconfig
// token rotated in its Secret) and nothing else would ever refresh them.
// This is wired into the Object reconciler, which will call this method on
// every reconcile to make resourceInformers aware of the referenced or managed
// resources of the given Object.
//
// Note that this complements cleanupResourceInformers which regularly
// garbage collects resource informers that are no longer referenced by
// any Object.
func (i *resourceInformers) WatchResources(rc *rest.Config, providerConfig, credentialsKey string, gvks ...schema.GroupVersionKind) { // nolint:gocyclo // we need to handle all cases.
	if rc == nil {
		rc = i.config
	}

	// start new informers
	for _, gvk := range gvks {
		key := gvkWithConfig{providerConfig: providerConfig, gvk: gvk}
		i.lock.RLock()
		existing, found := i.resourceCaches[key]
		i.lock.RUnlock()
		if found && existing.credentialsKey == credentialsKey {
			continue
		}

		log := i.log.WithValues("providerConfig", providerConfig, "gvk", gvk.String())

		ca, err := cache.New(rc, cache.Options{
			DefaultTransform: cache.TransformStripManagedFields(),
			DefaultWatchErrorHandler: func(_ context.Context, _ *kcache.Reflector, err error) {
				if errors.Is(err, io.EOF) {
					// Watch closed normally.
					return
				}
				// The reflector retries with a backoff of at most 30s, so
				// this is the only sign of a watch that keeps failing, e.g.
				// with 401 once the credentials it was started with expired.
				log.Info("Resource watch failed, retrying", "error", err)
			},
		})
		if err != nil {
			log.Info("failed creating a cache", "error", err)
			continue
		}

		// don't forget to call cancelFn in error cases to avoid leaks. In the
		// happy case it's called from the go routine starting the cache below.
		ctx, cancelFn := context.WithCancel(context.Background())

		obj := &metav1.PartialObjectMetadata{}
		obj.SetGroupVersionKind(gvk)
		inf, err := ca.GetInformer(ctx, obj, cache.BlockUntilSynced(false)) // don't block. We wait in the go routine below.
		if err != nil {
			cancelFn()
			log.Info("failed getting informer", "error", err)
			continue
		}

		if _, err := inf.AddEventHandler(kcache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				ev := runtimeevent.GenericEvent{
					Object: obj.(client.Object),
				}

				i.sink(providerConfig, ev)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				ev := runtimeevent.GenericEvent{
					Object: newObj.(client.Object),
				}

				i.sink(providerConfig, ev)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(kcache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				ev := runtimeevent.GenericEvent{
					Object: obj.(client.Object),
				}

				i.sink(providerConfig, ev)
			},
		}); err != nil {
			cancelFn()
			log.Info("failed adding event handler", "error", err)
			continue
		}

		go func() {
			defer cancelFn()

			log.Info("Starting resource watch")
			_ = ca.Start(ctx)
		}()

		i.lock.Lock()
		current, ok := i.resourceCaches[key]
		if ok && current.credentialsKey == credentialsKey {
			// Another goroutine already started the cache with these
			// credentials in parallel. We should cancel the new one.
			cancelFn()
			i.lock.Unlock()
			continue
		}
		if ok {
			// The cache was started with other credentials, which may have
			// expired since. Hand over to the new one.
			current.cancelFn()
			log.Info("Stopped resource watch, credentials changed")
		}
		i.resourceCaches[key] = resourceCache{
			cache:          ca,
			cancelFn:       cancelFn,
			credentialsKey: credentialsKey,
		}
		i.lock.Unlock()

		// wait for in the background.
		go func() {
			if synced := ca.WaitForCacheSync(ctx); synced {
				log.Debug("Resource cache synced")
			}
		}()
	}
}

// cleanupResourceInformers garbage collects resource informers that are
// no longer referenced by any Object. Ideally, all resource informers should
// stopped/cleaned up when the Object is deleted. However, in practice, this
// is not always the case. This method is a safety net to clean up resource
// informers that are no longer referenced by any Object.
func (i *resourceInformers) cleanupResourceInformers(ctx context.Context) {
	// copy map to avoid locking it for the entire duration of the loop
	i.lock.RLock()
	resourceCaches := make(map[gvkWithConfig]resourceCache, len(i.resourceCaches))
	for gc, ca := range i.resourceCaches {
		resourceCaches[gc] = ca
	}
	i.lock.RUnlock()

	// stop old informers
	i.log.Debug("Running garbage collection for resource informers", "count", len(i.resourceCaches))
	for gc, ca := range resourceCaches {
		list := v1alpha1.ObjectList{}
		if err := i.objectsCache.List(ctx, &list, client.MatchingFields{resourceRefGVKsIndex: refKeyProviderGVK(gc.providerConfig, gc.gvk.Kind, gc.gvk.Group, gc.gvk.Version)}); err != nil {
			i.log.Debug("cannot list objects referencing a certain resource GVK", "error", err, "fieldSelector", resourceRefGVKsIndex+"="+refKeyProviderGVK(gc.providerConfig, gc.gvk.Kind, gc.gvk.Group, gc.gvk.Version))
			continue
		}

		if len(list.Items) > 0 {
			continue
		}

		i.lock.Lock()
		current, ok := i.resourceCaches[gc]
		if !ok || current.cache != ca.cache {
			// WatchResources replaced the informer, or another run removed
			// it, since the map was copied. The replacement is considered on
			// the next run.
			i.lock.Unlock()
			continue
		}
		delete(i.resourceCaches, gc)
		i.lock.Unlock()

		ca.cancelFn()
		i.log.Info("Stopped resource watch", "provider config", gc.providerConfig, "gvk", gc.gvk)
	}
}

func parseAPIVersion(v string) (string, string) {
	parts := strings.SplitN(v, "/", 2)
	switch len(parts) {
	case 1:
		return "", parts[0]
	case 2:
		return parts[0], parts[1]
	default:
		return "", ""
	}
}
