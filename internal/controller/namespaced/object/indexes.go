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
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeevent "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane-contrib/provider-kubernetes/apis/namespaced/object/v1alpha1"
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
	_ client.IndexerFunc = IndexByProviderGVK
	_ client.IndexerFunc = IndexByProviderNamespacedNameGVK
)

// IndexByProviderGVK assumes the passed object is an Object. It returns keys
// with "ProviderConfig + GVK" for every resource referenced or managed by the
// Object.
func IndexByProviderGVK(o client.Object) []string {
	obj, ok := o.(*v1alpha1.Object)
	if !ok {
		return nil // should never happen
	}

	// Index references.
	refs := obj.Spec.References
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		refAPIVersion, refKind, _, _ := getReferenceInfo(ref)
		group, version := parseAPIVersion(refAPIVersion)
		providerConfig := "" // references are always local (i.e. on the control plane), which we represent as an empty provider config.
		keys = append(keys, refKeyProviderGVK(providerConfig, refKind, group, version))
	}

	// Index the desired object.
	// We don't expect errors here, as the parseManifest function is already called
	// in the reconciler and the desired object already validated.
	d, _ := parseManifest(obj)
	providerConfigKey := fmt.Sprintf("%s:%s:%s", obj.Spec.ProviderConfigReference.Name, obj.Spec.ProviderConfigReference.Kind, obj.GetNamespace())
	keys = append(keys, refKeyProviderGVK(providerConfigKey, d.GetKind(), d.GroupVersionKind().Group, d.GroupVersionKind().Version)) // unification is done by the informer.

	// unification is done by the informer.
	return keys
}

func refKeyProviderGVK(providerConfig, kind, group, version string) string {
	return fmt.Sprintf("%s.%s.%s.%s", providerConfig, kind, group, version)
}

// IndexByProviderNamespacedNameGVK assumes the passed object is an Object. It
// returns keys with "ProviderConfig + NamespacedName + GVK" for every resource
// referenced or managed by the Object.
func IndexByProviderNamespacedNameGVK(o client.Object) []string {
	obj, ok := o.(*v1alpha1.Object)
	if !ok {
		return nil // should never happen
	}

	// Index references.
	refs := obj.Spec.References
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		refAPIVersion, refKind, refNamespace, refName := getReferenceInfo(ref)
		providerConfig := "" // references are always local (i.e. on the control plane), which we represent as an empty provider config.
		keys = append(keys, refKeyProviderNamespacedNameGVK(providerConfig, refNamespace, refName, refKind, refAPIVersion))
	}

	// Index the desired object.
	// We don't expect errors here, as the parseManifest function is already called
	// in the reconciler and the desired object already validated.
	d, _ := parseManifest(obj)
	providerConfigKey := providerConfigRefKey(obj)
	keys = append(keys, refKeyProviderNamespacedNameGVK(providerConfigKey, d.GetNamespace(), d.GetName(), d.GetKind(), d.GetAPIVersion())) // unification is done by the informer.

	return keys
}

func providerConfigRefKey(obj *v1alpha1.Object) string {
	return fmt.Sprintf("%s/%s/%s", obj.Spec.ProviderConfigReference.Kind, obj.GetNamespace(), obj.Spec.ProviderConfigReference.Name)
}

func refKeyProviderNamespacedNameGVK(providerConfig, ns, name, kind, apiVersion string) string {
	return fmt.Sprintf("%s.%s.%s.%s.%s", providerConfig, name, ns, kind, apiVersion)
}

func enqueueObjectsForReferences(ca cache.Cache, log logging.Logger) func(ctx context.Context, ev runtimeevent.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	return func(ctx context.Context, ev runtimeevent.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		pc, _ := ctx.Value(keyProviderConfigName).(string)
		rGVK := ev.Object.GetObjectKind().GroupVersionKind()
		refKey := refKeyProviderNamespacedNameGVK(pc, ev.Object.GetNamespace(), ev.Object.GetName(), rGVK.Kind, rGVK.GroupVersion().String())

		objects := v1alpha1.ObjectList{}
		if err := ca.List(ctx, &objects, client.MatchingFields{resourceRefsIndex: refKey}); err != nil {
			log.Debug("cannot list objects related to a reference change", "error", err, "fieldSelector", resourceRefsIndex+"="+refKey)
			return
		}
		// queue those Objects for reconciliation
		for _, o := range objects.Items {
			// We only enqueue the Object if it has the Watch flag set to true.
			// Not every referencing Object watches the referenced resource.
			if o.Spec.Watch {
				log.Info("Enqueueing Object because referenced resource changed", "name", o.GetName(), "namespace", o.GetNamespace(), "referencedGVK", rGVK.String(), "referencedName", ev.Object.GetName(), "referencedNamespace", ev.Object.GetNamespace(), "providerConfig", pc)
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: o.GetName(), Namespace: o.GetNamespace()}})
			}
		}
	}
}
