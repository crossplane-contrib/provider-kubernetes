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

//go:build darwin && !cgo

package azure

import (
	"context"

	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	kconfig "github.com/crossplane-contrib/provider-kubernetes/pkg/kube/config"
)

// WrapRESTConfig is unavailable in cgo-less darwin builds: the token cache
// kubelogin v0.2 pulls in stores tokens in the macOS Keychain through cgo, so
// the azure package only compiles on darwin with cgo. Released provider images
// are linux builds and unaffected.
func WrapRESTConfig(_ context.Context, _ *rest.Config, _ []byte, identityType kconfig.IdentityType, _ ...string) error {
	return errors.Errorf("azure identity type %q needs a cgo-enabled build on darwin", identityType)
}
