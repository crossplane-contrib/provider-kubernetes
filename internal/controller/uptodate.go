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

// noiseFields are object fields that are managed by the API server or are
// otherwise mechanical, and therefore not meaningful "changes" when reporting
// what an update would do to a resource.
var noiseFields = [][]string{
	{"metadata", "managedFields"},
	{"metadata", "resourceVersion"},
	{"metadata", "uid"},
	{"metadata", "creationTimestamp"},
	{"metadata", "generation"},
	{"status"},
}

// UpdateModeDiff reports what would change on a live object (current) if the
// Object's manifest were applied in update mode. desiredObj is the result of a
// server-side apply dry run of the manifest as the provider field owner (with
// ForceOwnership) - i.e. exactly what the live object would become after an
// update-mode apply. The returned string is a cmp.Diff of the two, with
// server-managed and other mechanical fields removed so that only meaningful
// changes are shown.
//
// Because both objects have passed through server-side defaulting, defaulted
// values cancel out (see kubernetes/kubernetes#115563), and because a
// server-side apply leaves fields the manifest does not reference untouched,
// the diff contains exactly the fields the manifest would change - regardless
// of which field manager currently owns them. This answers the operator's
// question: "when I switch this Object to update mode, what actually changes?"
//
// It returns "" if either object is nil or if there is no meaningful
// difference.
func UpdateModeDiff(current, desiredObj *unstructured.Unstructured) string {
	if current == nil || desiredObj == nil {
		return ""
	}
	c := clean(current)
	d := clean(desiredObj)
	if equality.Semantic.DeepEqual(c, d) {
		return ""
	}
	return cmp.Diff(c, d)
}

// clean returns a deep copy of the object with server-managed and mechanical
// fields removed, so they do not appear as spurious changes in a diff.
func clean(u *unstructured.Unstructured) *unstructured.Unstructured {
	out := u.DeepCopy()
	for _, f := range noiseFields {
		unstructured.RemoveNestedField(out.Object, f...)
	}
	return out
}
