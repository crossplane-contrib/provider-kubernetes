#!/usr/bin/env bash
set -aeuo pipefail

# This script enables the --sanitize-secrets flag for the provider, then
# unpauses the MR, triggered by the uptest framework via
# `uptest.upbound.io/pre-assert-hook`: https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks

echo " ⏳ Enabling secret sanitization for the provider..."
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug", "--sanitize-secrets"]}]'

# Wait until the rollout is completed: every provider pod is Ready, and runs
# with the flag.
TIMEOUT_SECONDS=60
for (( i = 0; i < TIMEOUT_SECONDS; i++ )); do
  if ${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes -o json | jq -e '
      (.items | length) > 0 and all(.items[];
        .metadata.deletionTimestamp == null and
        any(.status.conditions[]?; .type == "Ready" and .status == "True") and
        (.spec.containers[] | select(.name == "package-runtime") | .args // [] | any(. == "--sanitize-secrets")))' > /dev/null; then
    break
  fi
  sleep 1
done
if (( i == TIMEOUT_SECONDS )); then
  echo " ❌ Timeout after ${TIMEOUT_SECONDS}s. The provider could not enable secret sanitization"
  exit 1
fi
current_provider_args=$(${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes -o jsonpath='{range .items[*]}{.spec.containers[?(@.name=="package-runtime")].args[*]}{"\n"}{end}')
echo " ⚙️ Current provider args: $current_provider_args"
echo " ☑️ Secret sanitization is enabled"

echo " ▶️ Unpausing the MR"
${KUBECTL} -n default annotate objects.kubernetes.m.crossplane.io foo-sanitized 'crossplane.io/paused=false' --overwrite
