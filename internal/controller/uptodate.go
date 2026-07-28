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

package controller

import (
	"github.com/google/go-cmp/cmp"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// UpToDate reports whether the observed state (last) of an Object's external
// resource matches the desired state, along with a diff describing any
// divergence.
//
// observeOnly indicates that the Object's management policies do not permit
// create or update actions (i.e. the resource is only observed). It is
// accepted so that callers in both the cluster-scoped and namespaced-scoped
// controllers share a single decision, without this package depending on
// either Object API version.
// UpToDate reports whether the observed state (last) of an Object's external
// resource matches the desired state, and, when it does not, a diff describing
// the divergence.
//
// The determination is based on state comparison alone: an Object is up-to-date
// exactly when its observed state equals its desired state. It is deliberately
// independent of the Object's management policies — an observe-only Object whose
// external resource has drifted is reported as not up-to-date, so that the
// divergence is surfaced (e.g. via the UpToDate condition) even though the
// reconciler will not act on it. See crossplane/crossplane-runtime#939.
//
// diff is a cmp.Diff of the observed versus desired state, suitable for a
// human-readable condition message. It is empty when the Object is up-to-date,
// and also when the observed state is unavailable (last == nil), to avoid
// emitting a misleading whole-object diff.
func UpToDate(last, desired *unstructured.Unstructured) (isUpToDate bool, diff string) {
	if last == nil {
		// Observed state could not be determined; report not up-to-date
		// without a (misleading) diff.
		return false, ""
	}
	if equality.Semantic.DeepEqual(last, desired) {
		return true, ""
	}
	return false, cmp.Diff(last, desired)
}
