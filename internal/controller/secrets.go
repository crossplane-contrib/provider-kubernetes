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
	"encoding/base64"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
)

const (
	// redactedValue replaces a value read from a Secret.
	redactedValue = "<redacted>"

	// minScrubLength is the length below which a value read from a Secret is
	// not scrubbed from error messages: replacing a short string everywhere
	// would mangle unrelated text, while hiding little.
	minScrubLength = 6

	errFmtSecretSourcedMetadata = "cannot patch %q from a Secret: with --sanitize-secrets, values read from a Secret are not stored in the Object, so they cannot set the metadata, name or namespace of its manifest"
)

// SecretSourcedPath returns the path of the manifest that a reference patches
// with a value read from an object of the given apiVersion and kind, and
// whether that object is a v1 Secret. Like ApplyFromFieldPathPatch, it
// defaults toFieldPath to fieldPath.
func SecretSourcedPath(apiVersion, kind string, fieldPath, toFieldPath *string) (string, bool) {
	if fieldPath == nil || !isSecret(apiVersion, kind) {
		return "", false
	}
	if toFieldPath != nil {
		return *toFieldPath, true
	}
	return *fieldPath, true
}

// ValidateSecretSourcedPath returns an error if a value read from a Secret
// would be patched into the metadata, name or namespace of a manifest. With
// secret sanitization these values are not stored in the Object, but deleting
// the object, which does not resolve references, needs them.
func ValidateSecretSourcedPath(path string) error {
	s, err := fieldpath.Parse(path)
	if err != nil {
		return err
	}
	if len(s) == 0 || (isField(s[0], "metadata") && (len(s) == 1 || isField(s[1], "name") || isField(s[1], "namespace"))) {
		return errors.Errorf(errFmtSecretSourcedMetadata, path)
	}
	return nil
}

// RedactSecretValues redacts the values read from a Secret from an observed
// object, in place:
//   - the value at each of paths that is present in u is replaced with
//     "<redacted>", unless ValidateSecretSourcedPath rejects the path;
//   - the data and stringData of a v1 Secret are replaced with a redaction
//     marker;
//   - the last applied configuration annotation, which holds the whole
//     manifest, is removed from a v1 Secret or if there are paths.
func RedactSecretValues(u *unstructured.Unstructured, paths []string) error {
	if err := redactPaths(u, paths); err != nil {
		return err
	}
	secret := isSecret(u.GetAPIVersion(), u.GetKind())
	if secret {
		if err := redactField(u, "data"); err != nil {
			return err
		}
		if _, ok := u.Object["stringData"]; ok {
			if err := redactField(u, "stringData"); err != nil {
				return err
			}
		}
	}
	if secret || len(paths) > 0 {
		annotations := u.GetAnnotations()
		delete(annotations, v1.LastAppliedConfigAnnotation)
		if len(annotations) == 0 {
			annotations = nil
		}
		u.SetAnnotations(annotations)
	}
	return nil
}

// LoggableObserved wraps an observed object for debug logging so that it is
// redacted with RedactSecretValues lazily, only when the log line is actually
// emitted.
func LoggableObserved(u *unstructured.Unstructured, paths []string) logr.Marshaler {
	return loggableObserved{u: u, paths: paths}
}

type loggableObserved struct {
	u     *unstructured.Unstructured
	paths []string
}

// MarshalLog implements logr.Marshaler.
func (l loggableObserved) MarshalLog() any {
	logged := l.u.DeepCopy()
	if err := RedactSecretValues(logged, l.paths); err != nil {
		// prefer dropping the object from the log line over leaking secret
		// data
		return nil
	}
	return logged
}

// RedactSecretManifest replaces the data and stringData contents of a raw
// v1 Secret manifest with a redaction marker. Non-Secret manifests and
// unparseable payloads are returned unchanged.
func RedactSecretManifest(raw []byte) ([]byte, bool) {
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(raw, u); err != nil || !isSecret(u.GetAPIVersion(), u.GetKind()) {
		return raw, false
	}
	redacted := false
	for _, field := range []string{"data", "stringData"} {
		if _, ok := u.Object[field]; ok {
			if err := redactField(u, field); err != nil {
				// prefer dropping the manifest from the log line over
				// leaking secret data
				return nil, true
			}
			redacted = true
		}
	}
	if !redacted {
		return raw, false
	}
	out, err := u.MarshalJSON()
	if err != nil {
		// should not happen for an object that was just unmarshalled; prefer
		// dropping the manifest from the log line over leaking secret data
		return nil, true
	}
	return out, true
}

// SecretValues are the values that references read from Secrets, in the
// forms they can take in an error message. The zero value is ready to use.
type SecretValues struct {
	forms sets.Set[string]
}

// Add records the value at fieldPath of a Secret: every string in it and,
// for a value of the Secret's data, its base64 decoded form.
func (s *SecretValues) Add(secret *unstructured.Unstructured, fieldPath string) {
	v, err := fieldpath.Pave(secret.Object).GetValue(fieldPath)
	if err != nil {
		return
	}
	segments, err := fieldpath.Parse(fieldPath)
	s.add(v, err == nil && len(segments) > 0 && isField(segments[0], "data"))
}

func (s *SecretValues) add(value any, data bool) {
	switch v := value.(type) {
	case string:
		s.addString(v)
		if decoded, err := base64.StdEncoding.DecodeString(v); data && err == nil {
			s.addString(string(decoded))
		}
	case map[string]any:
		for _, e := range v {
			s.add(e, data)
		}
	case []any:
		for _, e := range v {
			s.add(e, data)
		}
	}
}

// addString records a string, along with the escaped forms it takes when it
// is quoted in a message, e.g. in Invalid value: "...", or in JSON.
func (s *SecretValues) addString(v string) {
	if len(v) < minScrubLength {
		return
	}
	if s.forms == nil {
		s.forms = sets.New[string]()
	}
	s.forms.Insert(v)
	if q := strconv.Quote(v); len(q) > 2 {
		s.forms.Insert(q[1 : len(q)-1])
	}
	if j, err := json.Marshal(v); err == nil && len(j) > 2 {
		s.forms.Insert(string(j[1 : len(j)-1]))
	}
}

// scrub replaces every recorded value in msg with redactedValue.
func (s *SecretValues) scrub(msg string) string {
	if s.forms.Len() == 0 {
		return msg
	}
	// Longest first, so that a value is replaced as a whole rather than one
	// of its substrings that happens to be another recorded value.
	forms := s.forms.UnsortedList()
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
	oldnew := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		oldnew = append(oldnew, f, redactedValue)
	}
	return strings.NewReplacer(oldnew...).Replace(msg)
}

// ScrubError returns err with the recorded values scrubbed from its message.
// It returns err itself if there is nothing to scrub. Otherwise, the returned
// error does not wrap err, so that the values cannot be read from it.
func (s *SecretValues) ScrubError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if scrubbed := s.scrub(msg); scrubbed != msg {
		return errors.New(scrubbed)
	}
	return err
}

// isSecret returns whether apiVersion and kind are those of a v1 Secret.
func isSecret(apiVersion, kind string) bool {
	return apiVersion == "v1" && kind == "Secret"
}

func isField(s fieldpath.Segment, name string) bool {
	return s.Type == fieldpath.SegmentField && s.Field == name
}

// redactPaths replaces the value at each of paths that is present in u, and
// that ValidateSecretSourcedPath accepts, with redactedValue.
func redactPaths(u *unstructured.Unstructured, paths []string) error {
	p := fieldpath.Pave(u.Object)
	for _, path := range paths {
		if ValidateSecretSourcedPath(path) != nil {
			continue
		}
		if _, err := p.GetValue(path); err != nil {
			// Not present, so there is nothing to redact.
			continue
		}
		if err := p.SetValue(path, redactedValue); err != nil {
			return err
		}
	}
	return nil
}

// redactField replaces the contents of the named top-level field with a
// redaction marker.
func redactField(u *unstructured.Unstructured, field string) error {
	data := map[string][]byte{"redacted": []byte(nil)}
	return fieldpath.Pave(u.Object).SetValue(field, data)
}
