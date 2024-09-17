#!/usr/bin/env bash
set -aeuo pipefail

# This script is used to validate the watch feature of the object, triggered by the
# uptest framework via `uptest.upbound.io/post-assert-hook`: https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks
KUBECTL="kubectl"

VALUE=$(${KUBECTL} get secret bar -o jsonpath='{.data.key}' | base64 -d)
if [ "${VALUE}" == "new-value" ]; then
  echo "This test has to pass in the first run since we're validating the realtime watching behaviour."
  exit 1
fi

echo "Enabling watch feature for the provider"
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug", "--enable-watches"]}]'

sleep 30

echo "Patching referenced secret"
${KUBECTL} patch secret bar --type='merge' -p='{"stringData":{"key":"new-value"}}'

sleep 3

echo "Checking if the managed secret has been updated"
VALUE=$(${KUBECTL} get secret foo -o jsonpath='{.data.key-from-bar}' | base64 -d)
if [ "${VALUE}" != "new-value" ]; then
  echo "Expected value to be 'new-value' but got '${VALUE}'"
  exit 1
fi
echo "Checking if the managed secret has been updated...Success"

echo "Patching managed secret"
${KUBECTL} patch secret foo --type='merge' -p='{"stringData":{"a-new-key":"with-new-value"}}'

sleep 3

echo "Checking if the object grabbed the new value at status.atProvider"
VALUE=$(${KUBECTL} get object foo -o jsonpath='{.status.atProvider.manifest.data.a-new-key}' | base64 -d)

if [ "${VALUE}" != "with-new-value" ]; then
  echo "Expected value to be 'with-new-value' but got '${VALUE}'"
  exit 1
fi
echo "Checking if the object grabbed the new value at status.atProvider...Success"

# TODO(turkenh): Add one more test case to validate the drift is reverted back to the desired state
# in realtime after https://github.com/crossplane-contrib/provider-kubernetes/issues/37 is resolved.

echo "Successfully validated the watch feature!"

echo "Disabling watch feature for the provider"
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug"]}]'

