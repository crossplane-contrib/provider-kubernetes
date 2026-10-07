#!/usr/bin/env bash
set -aeuo pipefail

# This script validates that, with the --sanitize-secrets flag, a value that
# the MR patches from a Secret reaches the target object, but is stored
# neither in the MR nor in its events, also after the Secret is rotated.
# It is triggered by the uptest framework via `uptest.upbound.io/post-assert-hook`: https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks

OBJECT="objects.kubernetes.m.crossplane.io/foo-sanitized"

# Waits until the target Secret has the given value.
validate_target() {
  for (( i = 0; i < 30; i++ )); do
    VALUE=$(${KUBECTL} -n default get secret foo-sanitized -o jsonpath='{.data.password}' | base64 -d || true)
    if [ "${VALUE}" == "$1" ]; then
      echo " ✅ The target Secret has the value"
      return
    fi
    sleep 1
  done
  echo " ❌ Expected the target Secret to have '$1' but got '${VALUE}'"
  exit 1
}

# Validates that neither the MR (spec and status) nor its events contain any
# of the given values, plain or base64 encoded.
validate_object() {
  OBJECT_UID=$(${KUBECTL} -n default get "${OBJECT}" -o jsonpath='{.metadata.uid}')
  OBSERVED=$(${KUBECTL} -n default get "${OBJECT}" -o json; ${KUBECTL} -n default get events --field-selector "involvedObject.uid=${OBJECT_UID}" -o json)
  for VALUE in "$@"; do
    for FORM in "${VALUE}" "$(printf '%s' "${VALUE}" | base64)"; do
      if grep -qF -- "${FORM}" <<< "${OBSERVED}"; then
        echo " ❌ The MR or its events contain '${FORM}'"
        ${KUBECTL} -n default get "${OBJECT}" -o jsonpath='{.spec.forProvider.manifest}{"\n"}'
        exit 1
      fi
    done
  done
  echo " ✅ Neither the MR nor its events contain the value"
}

echo " ⏳ Validating the value from the Secret..."
validate_target sample-password
validate_object sample-password

echo " 🔄 Rotating the referenced Secret"
${KUBECTL} -n default patch secret bar-sanitized --type='merge' -p='{"stringData":{"password":"rotated-password"}}'

echo " ⏳ Validating the rotated value from the Secret..."
validate_target rotated-password
validate_object sample-password rotated-password

echo " ✅ Successfully validated secret sanitization!"

echo " ⏳ Disabling secret sanitization for the provider..."
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug"]}]'

# Wait until the rollout is completed, so that the next tests find the
# provider as they expect it.
TIMEOUT_SECONDS=60
for (( i = 0; i < TIMEOUT_SECONDS; i++ )); do
  if ${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes -o json | jq -e '
      (.items | length) > 0 and all(.items[];
        .metadata.deletionTimestamp == null and
        any(.status.conditions[]?; .type == "Ready" and .status == "True") and
        (.spec.containers[] | select(.name == "package-runtime") | .args // [] | any(. == "--sanitize-secrets") | not))' > /dev/null; then
    break
  fi
  sleep 1
done
if (( i == TIMEOUT_SECONDS )); then
  echo " ❌ Timeout after ${TIMEOUT_SECONDS}s. The provider could not disable secret sanitization"
  exit 1
fi
current_provider_args=$(${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes -o jsonpath='{range .items[*]}{.spec.containers[?(@.name=="package-runtime")].args[*]}{"\n"}{end}')
echo " ⚙️ Current provider args: $current_provider_args"
echo " ☑️ Secret sanitization is disabled"
