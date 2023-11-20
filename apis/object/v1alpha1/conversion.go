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
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"

	"github.com/crossplane-contrib/provider-kubernetes/apis/object/v1beta1"
)

// ConvertTo converts this Object to the Hub version (v1beta1).
func (src *Object) ConvertTo(dstRaw conversion.Hub) error { // nolint:golint // We want to use different names for receiver parameter to be more clear.
	dst := dstRaw.(*v1beta1.Object)

	// copy identical fields
	dst.ObjectMeta = src.ObjectMeta

	// handle management policies migration
	switch src.Spec.ManagementPolicy {
	case Default:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionAll}
	case ObserveCreateUpdate:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve, xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate}
	case ObserveDelete:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve, xpv1.ManagementActionDelete}
	case Observe:
		dst.Spec.ManagementPolicies = xpv1.ManagementPolicies{xpv1.ManagementActionObserve}
	default:
		return errors.New("unknown management policy")
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1beta1) to this version.
func (dst *Object) ConvertFrom(srcRaw conversion.Hub) error { // nolint:golint // We want to use different names for receiver parameter to be more clear.
	src := srcRaw.(*v1beta1.Object)

	// copy identical fields
	dst.ObjectMeta = src.ObjectMeta

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
		// TODO(turkenh): Should we default to something here instead of erroring out?
		return errors.New("unsupported management policy")
	}

	return nil
}
