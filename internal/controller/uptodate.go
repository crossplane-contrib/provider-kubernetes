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
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/go-cmp/cmp"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// maxDiffLen bounds the length of a rendered diff so it stays well within the
// size of a status condition message. Diffs longer than this are truncated with
// a trailing marker.
const maxDiffLen = 4096

// redactedValue is shown in place of a sensitive value (e.g. Secret data) in a
// diff, so the diff reveals which fields would change without leaking their
// contents.
const redactedValue = "<redacted>"

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
// update-mode apply. The returned string lists each changed field by its full
// dotted key path, with server-managed and other mechanical fields removed so
// that only meaningful changes are shown.
//
// Because both objects have passed through server-side defaulting, defaulted
// values cancel out (see kubernetes/kubernetes#115563), and because a
// server-side apply leaves fields the manifest does not reference untouched,
// the diff contains exactly the fields the manifest would change - regardless
// of which field manager currently owns them. This answers the operator's
// question: "when I switch this Object to update mode, what actually changes?"
//
// The output is path-oriented rather than line-oriented: each line is
// "~ path: old -> new", "+ path: new", or "- path: old", using the object's own
// key paths (e.g. "data.sample-key", "metadata.labels.env"). This is stable
// regardless of key ordering and needs no line numbers, so it can be read
// against a GitOps manifest directly.
//
// Sensitive values are redacted (see redactSensitive): for a v1 Secret, the
// values under data/stringData are shown as "<redacted>", so the diff reveals
// which keys would change without exposing their contents. This redaction is
// unconditional - unlike setAtProvider's Secret sanitization, which is gated on
// the --sanitize-secrets flag. The divergence is deliberate: a condition
// message is surfaced through the Kubernetes event/status stream, which is
// typically far more visible (and less access-controlled) than an RBAC-gated
// read of the Secret object itself, so leaking values here would be a
// materially worse exposure than in status.atProvider. We therefore always
// redact in the diff regardless of the flag.
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

	r := &pathDiffReporter{redactValue: sensitiveValuePath(desiredObj)}
	cmp.Diff(c.Object, d.Object, cmp.Reporter(r))
	return truncate(r.String(), maxDiffLen)
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

// sensitiveValuePath returns a predicate reporting whether a given key path
// points at a value whose contents must be redacted in a diff. For a v1 Secret,
// the values under data and stringData are sensitive.
func sensitiveValuePath(u *unstructured.Unstructured) func(path []string) bool {
	if u.GetKind() != "Secret" || u.GetAPIVersion() != "v1" {
		return func([]string) bool { return false }
	}
	return func(path []string) bool {
		return len(path) >= 2 && (path[0] == "data" || path[0] == "stringData")
	}
}

// pathDiffReporter is a cmp.Reporter that records each differing leaf as a line
// keyed by its full dotted map-key path, redacting values whose path satisfies
// redactValue.
type pathDiffReporter struct {
	path        cmp.Path
	lines       []string
	redactValue func(path []string) bool
}

func (r *pathDiffReporter) PushStep(s cmp.PathStep) { r.path = append(r.path, s) }
func (r *pathDiffReporter) PopStep()                { r.path = r.path[:len(r.path)-1] }

func (r *pathDiffReporter) Report(rs cmp.Result) {
	if rs.Equal() {
		return
	}
	keyPath := r.keyPath()
	vx, vy := r.path.Last().Values()
	redact := r.redactValue != nil && r.redactValue(keyPath)
	key := strings.Join(keyPath, ".")
	switch {
	case !vx.IsValid():
		r.lines = append(r.lines, fmt.Sprintf("+ %s: %s", key, renderValue(vy, redact)))
	case !vy.IsValid():
		r.lines = append(r.lines, fmt.Sprintf("- %s: %s", key, renderValue(vx, redact)))
	default:
		r.lines = append(r.lines, fmt.Sprintf("~ %s: %s -> %s", key, renderValue(vx, redact), renderValue(vy, redact)))
	}
}

// keyPath renders the map-key portion of the current path (ignoring type/struct
// steps), e.g. ["data", "sample-key"].
func (r *pathDiffReporter) keyPath() []string {
	var parts []string
	for _, s := range r.path {
		if mi, ok := s.(cmp.MapIndex); ok {
			parts = append(parts, fmt.Sprintf("%v", mi.Key()))
		}
	}
	return parts
}

func (r *pathDiffReporter) String() string {
	sort.Strings(r.lines)
	return strings.Join(r.lines, "\n")
}

func renderValue(v reflect.Value, redact bool) string {
	if redact {
		return redactedValue
	}
	if !v.IsValid() {
		return "<none>"
	}
	return fmt.Sprintf("%v", v.Interface())
}

// truncate bounds s to max characters, appending a marker when it is cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "\n... (diff truncated)"
	if max <= len(marker) {
		return s[:max]
	}
	return s[:max-len(marker)] + marker
}
