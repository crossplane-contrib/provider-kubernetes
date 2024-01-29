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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1alpha2"
)

// ConvertTo converts this Object to the Hub version (v1alpha2).
func (src *Object) ConvertTo(dstRaw conversion.Hub) error { // nolint:golint // We want to use different names for receiver parameter to be more clear.
	dst := dstRaw.(*v1alpha2.Object)

	// copy identical fields
	dst.ObjectMeta = src.ObjectMeta

	dst.Status = v1alpha2.ObjectStatus{
		ResourceStatus: src.Status.ResourceStatus,
		AtProvider: v1alpha2.ObjectObservation{
			Manifest: src.Status.AtProvider.Manifest,
		},
	}

	connectionDetails := []v1alpha2.ConnectionDetail{}
	for _, cd := range src.Spec.ConnectionDetails {
		connectionDetails = append(connectionDetails, v1alpha2.ConnectionDetail{
			ObjectReference: cd.ObjectReference,
		})
	}

	references := []v1alpha2.Reference{}
	for _, r := range src.Spec.References {
		ref := v1alpha2.Reference{}
		if r.DependsOn != nil {
			ref.DependsOn = &v1alpha2.DependsOn{
				APIVersion: r.DependsOn.APIVersion,
				Kind:       r.DependsOn.Kind,
				Name:       r.DependsOn.Name,
				Namespace:  r.DependsOn.Namespace,
			}
		}
		if r.PatchesFrom != nil {
			ref.PatchesFrom = &v1alpha2.PatchesFrom{
				DependsOn: v1alpha2.DependsOn{
					APIVersion: r.PatchesFrom.APIVersion,
					Kind:       r.PatchesFrom.Kind,
					Name:       r.PatchesFrom.Name,
					Namespace:  r.PatchesFrom.Namespace,
				},
				FieldPath: r.PatchesFrom.FieldPath,
			}
		}
		ref.ToFieldPath = r.ToFieldPath
		references = append(references, ref)
	}

	dst.Spec = v1alpha2.ObjectSpec{
		ResourceSpec: xpv1.ResourceSpec{
			WriteConnectionSecretToReference: src.GetWriteConnectionSecretToReference(),
			PublishConnectionDetailsTo:       src.GetPublishConnectionDetailsTo(),
			ProviderConfigReference:          src.GetProviderConfigReference(),
			DeletionPolicy:                   src.GetDeletionPolicy(),
		},
		ConnectionDetails: connectionDetails,
		ForProvider: v1alpha2.ObjectParameters{
			Manifest: src.Spec.ForProvider.Manifest,
		},
		References: references,
		Readiness: v1alpha2.Readiness{
			Policy: v1alpha2.ReadinessPolicy(src.Spec.Readiness.Policy),
		},
	}

	// handle management policies migration
	switch src.Spec.ManagementPolicy {
	case Default, "":
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionAll}
	case ObserveCreateUpdate:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve, xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate}
	case ObserveDelete:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve, xpv1.ManagementActionDelete}
	case Observe:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve}
	default:
		return errors.Errorf("unknown management policy: %v", src.Spec.ManagementPolicy)
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1alpha2) to this version.
func (dst *Object) ConvertFrom(srcRaw conversion.Hub) error { // nolint:golint, gocyclo // We want to use different names for receiver parameter to be more clear.
	src := srcRaw.(*v1alpha2.Object)

	// copy identical fields
	dst.ObjectMeta = src.ObjectMeta
	dst.Status = ObjectStatus{
		ResourceStatus: src.Status.ResourceStatus,
		AtProvider: ObjectObservation{
			Manifest: src.Status.AtProvider.Manifest,
		},
	}

	connectionDetails := []ConnectionDetail{}
	for _, cd := range src.Spec.ConnectionDetails {
		connectionDetails = append(connectionDetails, ConnectionDetail{
			ObjectReference: cd.ObjectReference,
		})
	}

	references := []Reference{}
	for _, r := range src.Spec.References {
		ref := Reference{}
		if r.DependsOn != nil {
			ref.DependsOn = &DependsOn{
				APIVersion: r.DependsOn.APIVersion,
				Kind:       r.DependsOn.Kind,
				Name:       r.DependsOn.Name,
				Namespace:  r.DependsOn.Namespace,
			}
		}
		if r.PatchesFrom != nil {
			ref.PatchesFrom = &PatchesFrom{
				DependsOn: DependsOn{
					APIVersion: r.PatchesFrom.APIVersion,
					Kind:       r.PatchesFrom.Kind,
					Name:       r.PatchesFrom.Name,
					Namespace:  r.PatchesFrom.Namespace,
				},
				FieldPath: r.PatchesFrom.FieldPath,
			}
		}
		ref.ToFieldPath = r.ToFieldPath
		references = append(references, ref)
	}

	dst.Spec = ObjectSpec{
		ResourceSpec: ResourceSpec{
			WriteConnectionSecretToReference: src.GetWriteConnectionSecretToReference(),
			PublishConnectionDetailsTo:       src.GetPublishConnectionDetailsTo(),
			ProviderConfigReference:          src.GetProviderConfigReference(),
			DeletionPolicy:                   src.GetDeletionPolicy(),
		},
		ConnectionDetails: connectionDetails,
		ForProvider: ObjectParameters{
			Manifest: src.Spec.ForProvider.Manifest,
		},
		References: references,
		Readiness: Readiness{
			Policy: ReadinessPolicy(src.Spec.Readiness.Policy),
		},
	}

	// Policies are unset and the object is not yet created.
	// As the managementPolicies would default to ["*"], we can set
	// the management policy to Default.
	if src.GetManagementPolicies() == nil && src.CreationTimestamp.IsZero() {
		dst.Spec.ManagementPolicy = Default
		return nil
	}

	// handle management policies migration
	policySet := sets.New[xpv1.ManagementAction](src.GetManagementPolicies()...)

	switch {
	case policySet.Has(xpv1.ManagementActionAll):
		dst.Spec.ManagementPolicy = Default
	case policySet.HasAll(xpv1.ManagementActionObserve, xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate, xpv1.ManagementActionDelete):
		dst.Spec.ManagementPolicy = Default
	case policySet.HasAll(xpv1.ManagementActionObserve, xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate) &&
		!policySet.Has(xpv1.ManagementActionDelete):
		dst.Spec.ManagementPolicy = ObserveCreateUpdate
	case policySet.HasAll(xpv1.ManagementActionObserve, xpv1.ManagementActionDelete) &&
		!policySet.HasAny(xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate):
		dst.Spec.ManagementPolicy = ObserveDelete
	case policySet.Has(xpv1.ManagementActionObserve) &&
		!policySet.HasAny(xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate, xpv1.ManagementActionDelete):
		dst.Spec.ManagementPolicy = Observe
	default:
		// NOTE(lsviben): Other combinations of v1alpha2 management policies
		// were not supported in v1alpha1. Leaving it empty to avoid
		// errors during conversion instead of failing.
	}

	return nil
}
