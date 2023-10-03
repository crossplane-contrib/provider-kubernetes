package fake

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An UnstructuredExtractor mocks an UnstructuredExtractor.
// https://pkg.go.dev/k8s.io/client-go/applyconfigurations/meta/v1#UnstructuredExtractor
type UnstructuredExtractor struct {
	ExtractFn       func(object *unstructured.Unstructured, fieldManager string) (*unstructured.Unstructured, error)
	ExtractStatusFn func(object *unstructured.Unstructured, fieldManager string) (*unstructured.Unstructured, error)
}

// Extract mocks the Extract method of an UnstructuredExtractor
func (u *UnstructuredExtractor) Extract(object *unstructured.Unstructured, fieldManager string) (*unstructured.Unstructured, error) {
	return u.ExtractFn(object, fieldManager)
}

// ExtractStatus mocks the ExtractStatus method of an UnstructuredExtractor
func (u *UnstructuredExtractor) ExtractStatus(object *unstructured.Unstructured, fieldManager string) (*unstructured.Unstructured, error) {
	return u.ExtractStatusFn(object, fieldManager)
}
