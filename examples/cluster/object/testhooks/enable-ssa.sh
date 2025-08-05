#!/usr/bin/env bash
set -aeuo pipefail

echo "Enabling ssa feature for the provider"
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug", "--enable-server-side-apply"]}]'
