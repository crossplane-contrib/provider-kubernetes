# Project Setup
PROJECT_NAME := provider-kubernetes
PROJECT_REPO := github.com/crossplane-contrib/$(PROJECT_NAME)

PLATFORMS ?= linux_amd64 linux_arm64

# -include will silently skip missing files, which allows us
# to load those files with a target in the Makefile. If only
# "include" was used, the make command would fail and refuse
# to run a target until the include commands succeeded.
-include build/makelib/common.mk

# ====================================================================================
# Setup Output

-include build/makelib/output.mk

# ====================================================================================
# Setup Go

# Set a sane default so that the nprocs calculation below is less noisy on the initial
# loading of this file
NPROCS ?= 1

# each of our test suites starts a kube-apiserver and running many test suites in
# parallel can lead to high CPU utilization. by default we reduce the parallelism
# to half the number of CPU cores.
GO_TEST_PARALLEL := $(shell echo $$(( $(NPROCS) / 2 )))

GO_STATIC_PACKAGES = $(GO_PROJECT)/cmd/provider
GO_SUBDIRS += cmd internal apis
GO111MODULE = on
GOLANGCILINT_VERSION = 1.55.2
-include build/makelib/golang.mk

# ====================================================================================
# Setup Kubernetes tools
KIND_VERSION = v0.22.0
UP_VERSION = v0.28.0
UPTEST_VERSION = v0.9.0
UP_CHANNEL = stable
USE_HELM3 = true
-include build/makelib/k8s_tools.mk

# ====================================================================================
# Setup Images

IMAGES = provider-kubernetes
-include build/makelib/imagelight.mk

# ====================================================================================
# Targets

# run `make help` to see the targets and options

# We want submodules to be set up the first time `make` is run.
# We manage the build/ folder and its Makefiles as a submodule.
# The first time `make` is run, the includes of build/*.mk files will
# all fail, and this target will be run. The next time, the default as defined
# by the includes will be run instead.
fallthrough: submodules
	@echo Initial setup complete. Running make again . . .
	@make

# ====================================================================================
# Setup XPKG

XPKG_REG_ORGS ?= xpkg.upbound.io/crossplane-contrib index.docker.io/crossplanecontrib
# NOTE(hasheddan): skip promoting on xpkg.upbound.io as channel tags are
# inferred.
XPKG_REG_ORGS_NO_PROMOTE ?= xpkg.upbound.io/crossplane-contrib
XPKGS = provider-kubernetes
-include build/makelib/xpkg.mk

# NOTE(hasheddan): we force image building to happen prior to xpkg build so that
# we ensure image is present in daemon.
xpkg.build.provider-kubernetes: do.build.images

# Generate a coverage report for cobertura applying exclusions on
# - generated file
cobertura:
	@cat $(GO_TEST_OUTPUT)/coverage.txt | \
		grep -v zz_generated.deepcopy | \
		$(GOCOVER_COBERTURA) > $(GO_TEST_OUTPUT)/cobertura-coverage.xml

# ====================================================================================
# End to End Testing
CROSSPLANE_NAMESPACE = crossplane-system
-include build/makelib/local.xpkg.mk
-include build/makelib/controlplane.mk

# TODO(turkenh): Add "examples/object/object-ssa-owner.yaml" to the list to test the SSA functionality as part of the e2e tests.
# The test is disabled for now because uptest clears the package cache when the provider restarted with the SSA flag.
# Enable after https://github.com/crossplane/uptest/issues/17 is fixed.
UPTEST_EXAMPLE_LIST ?= "examples/object/object.yaml,examples/object/object-watching.yaml"
uptest: $(UPTEST) $(KUBECTL) $(KUTTL)
	@$(INFO) running automated tests
	@KUBECTL=$(KUBECTL) KUTTL=$(KUTTL) CROSSPLANE_NAMESPACE=${CROSSPLANE_NAMESPACE} $(UPTEST) e2e "$(UPTEST_EXAMPLE_LIST)" --setup-script=cluster/test/setup.sh || $(FAIL)
	@$(OK) running automated tests

local-dev: controlplane.up
local-deploy: build controlplane.up local.xpkg.deploy.provider.$(PROJECT_NAME)
	@$(INFO) running locally built provider
	@$(KUBECTL) wait provider.pkg $(PROJECT_NAME) --for condition=Healthy --timeout 5m
	@$(KUBECTL) -n $(CROSSPLANE_NAMESPACE) wait --for=condition=Available deployment --all --timeout=5m
	@$(OK) running locally built provider

e2e: local-deploy uptest
# Update the submodules, such as the common build scripts.
submodules:
	@git submodule sync
	@git submodule update --init --recursive

# NOTE(hasheddan): we must ensure up is installed in tool cache prior to build
# as including the k8s_tools machinery prior to the xpkg machinery sets UP to
# point to tool cache.
build.init: $(UP)

# This is for running out-of-cluster locally, and is for convenience. Running
# this make target will print out the command which was used. For more control,
# try running the binary directly with different arguments.
run: $(KUBECTL) generate
	@$(INFO) Running Crossplane locally out-of-cluster . . .
	@$(KUBECTL) apply -f package/crds/ -R
	go run cmd/provider/main.go -d

manifests:
	@$(INFO) Deprecated. Run make generate instead.

.PHONY: cobertura submodules fallthrough test-integration run manifests

generate.run: go.generate kustomize.gen

generate.done: kustomize.clean

# This hack is needed because we want to inject the conversion webhook
# configuration into the Object CRD. This is not possible with the CRD
# generation through controller-gen, and actually kubebuilder does
# something similar, so we are following suit. Can be removed once we
# drop support for v1alpha1.
kustomize.gen: $(KUBECTL)
	@$(INFO) Generating CRDs with kustomize
	@$(KUBECTL) kustomize cluster/kustomize/ > cluster/kustomize/kubernetes.crossplane.io_objects.yaml
	@mv cluster/kustomize/kubernetes.crossplane.io_objects.yaml cluster/kustomize/crds/kubernetes.crossplane.io_objects.yaml
	@mv cluster/kustomize/crds package/crds
	@$(OK) Generated CRDs with kustomize

kustomize.clean:
	@$(INFO) Cleaning up kustomize generated CRDs
	@rm -rf cluster/kustomize/crds
	@rm -f cluster/kustomize/kubernetes.crossplane.io_objects.yaml
	@$(OK) Cleaned up kustomize generated CRDs
