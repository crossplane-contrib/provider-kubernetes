//go:build generate
// +build generate

// Generate deepcopy methodsets
//go:generate go run -tags generate sigs.k8s.io/controller-tools/cmd/controller-gen object:headerFile=../../../hack/boilerplate.go.txt paths=.

package config
