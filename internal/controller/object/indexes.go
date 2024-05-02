/*
Copyright 2023 The Crossplane Authors.

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
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/pkg/logging"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

const (
	// resourceRefGVKsIndex is an index of all GroupKinds that
	// are in use by an Object.
	resourceRefGVKsIndex = "objectsRefsGVKs"
	// resourceRefsIndex is an index of resourceRefs that are referenced or
	// managed by an Object.
	resourceRefsIndex = "objectsRefs"
)

var (
	_ client.IndexerFunc = IndexReferencedResourceRefGVKs
	_ client.IndexerFunc = IndexReferencesResourcesRefs
)

// IndexReferencedResourceRefGVKs assumes the passed object is an Object. It
// returns gvk keys for every resource referenced or managed by the Object.
func IndexReferencedResourceRefGVKs(o client.Object) []string {
	obj, ok := o.(*v1alpha2.Object)
	if !ok {
		return nil // should never happen
	}

	// Index references.
	refs := obj.Spec.References
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		refAPIVersion, refKind, _, _ := getReferenceInfo(ref)
		group, version := parseAPIVersion(refAPIVersion)
		// References are always on control plane, so we don't pass the config name.
		keys = append(keys, refKeyGKV("", refKind, group, version))
	}

	// Index the desired object.
	// We don't expect errors here, as the getDesired function is already called
	// in the reconciler and the desired object already validated.
	d, _ := getDesired(obj)
	keys = append(keys, refKeyGKV(obj.Spec.ProviderConfigReference.Name, d.GetKind(), d.GroupVersionKind().Group, d.GroupVersionKind().Version)) // unification is done by the informer.

	// unification is done by the informer.
	return keys
}

func refKeyGKV(providerConfig, kind, group, version string) string {
	return fmt.Sprintf("%s.%s.%s.%s", providerConfig, kind, group, version)
}

// IndexReferencesResourcesRefs assumes the passed object is an Object. It
// returns keys for every resource referenced or managed by the Object.
func IndexReferencesResourcesRefs(o client.Object) []string {
	obj, ok := o.(*v1alpha2.Object)
	if !ok {
		return nil // should never happen
	}

	// Index references.
	refs := obj.Spec.References
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		refAPIVersion, refKind, refNamespace, refName := getReferenceInfo(ref)
		// References are always on control plane, so we don't pass the provider config name.
		keys = append(keys, refKey("", refNamespace, refName, refKind, refAPIVersion))
	}

	// Index the desired object.
	// We don't expect errors here, as the getDesired function is already called
	// in the reconciler and the desired object already validated.
	d, _ := getDesired(obj)
	keys = append(keys, refKey(obj.Spec.ProviderConfigReference.Name, d.GetNamespace(), d.GetName(), d.GetKind(), d.GetAPIVersion())) // unification is done by the informer.

	return keys
}

func refKey(providerConfig, ns, name, kind, apiVersion string) string {
	return fmt.Sprintf("%s.%s.%s.%s.%s", providerConfig, name, ns, kind, apiVersion)
}

func enqueueObjectsForReferences(ca cache.Cache, log logging.Logger) func(ctx context.Context, ev runtimeevent.UpdateEvent, q workqueue.RateLimitingInterface) {
	return func(ctx context.Context, ev runtimeevent.UpdateEvent, q workqueue.RateLimitingInterface) {
		// "pc" is the provider pc name. It will be empty for referenced
		// resources, as they are always on the control plane.
		pc, _ := ctx.Value(keyProviderConfigName).(string)
		rGVK := ev.ObjectNew.GetObjectKind().GroupVersionKind()
		key := refKey(pc, ev.ObjectNew.GetNamespace(), ev.ObjectNew.GetName(), rGVK.Kind, rGVK.GroupVersion().String())

		objects := v1alpha2.ObjectList{}
		if err := ca.List(ctx, &objects, client.MatchingFields{resourceRefsIndex: key}); err != nil {
			log.Debug("cannot list objects related to a reference change", "error", err, "fieldSelector", resourceRefsIndex+"="+key)
			return
		}
		// queue those Objects for reconciliation
		for _, o := range objects.Items {
			log.Info("Enqueueing Object because referenced resource changed", "name", o.GetName(), "referencedGVK", rGVK.String(), "referencedName", ev.ObjectNew.GetName(), "providerConfig", pc)
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: o.GetName()}})
		}
	}
}
