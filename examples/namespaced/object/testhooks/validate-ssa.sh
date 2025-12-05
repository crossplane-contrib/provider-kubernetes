#!/usr/bin/env bash
set -aeuo pipefail

# This script is used to validate the ssa feature, triggered by the
# uptest framework via `uptest.upbound.io/post-assert-hook`: https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks

LABELER_OBJECT_MANIFEST=$(cat <<'EOF'
apiVersion: kubernetes.crossplane.io/v1alpha2
kind: Object
metadata:
  name: sample-service-labeler
  namespace: default
spec:
  # Note: This resource will only patch/update the manifest below.
  # It will not delete or create the resource.
  # As a limitation, it will not clean up the changes it made during its deletion.
  # This requires the Server Side Apply feature to be enabled in the provider
  # with the --enable-server-side-apply flag.
  managementPolicies: ["Observe", "Update"]
  forProvider:
    manifest:
      apiVersion: v1
      kind: Service
      metadata:
        name: sample-service
        namespace: default
        labels:
          another-key: another-value
  providerConfigRef:
    name: kubernetes-provider
EOF
)

printf "%s" "$LABELER_OBJECT_MANIFEST" | ${KUBECTL} apply -f-
printf "%s" "$LABELER_OBJECT_MANIFEST" | ${KUBECTL} wait -f- --for condition=ready --timeout=1m

last_applied_annotation=$(${KUBECTL} get service sample-service -o jsonpath="{.metadata.annotations['kubectl.kubernetes.io/last-applied-configuration']}")
if [ -n "$last_applied_annotation" ]; then # This annotation should not be present when SSA is enabled
  echo "SSA validation failed! Annotation 'last-applied-configuration' should not exist when SSA is enabled!"
  exit 1
fi

somekey_value=$(${KUBECTL} get service sample-service -o jsonpath='{.metadata.labels.some-key}')
anotherkey_value=$(${KUBECTL} get service sample-service -o jsonpath='{.metadata.labels.another-key}')
if [ "$somekey_value" != "some-value" ] || [ "$anotherkey_value" != "another-value" ]; then
  echo "SSA validation failed! Labels 'some-key' and 'another-key' from both Objects should exist with values 'some-value' and 'another-value' respectively!"
  exit 1
fi
echo "Successfully validated the SSA feature!"

printf "%s" "$LABELER_OBJECT_MANIFEST" | ${KUBECTL} delete -f-

