#!/usr/bin/env bash
set -aeuo pipefail

# This script validates that SSA correctly handles the case
# at https://github.com/kubernetes/kubernetes/issues/99003

# At this point, we have SSA disabled.
# From the Object MR, we remove a label named unwanted
echo " ⏳ Removing the unwanted label from desired manifest in Object (SSA disabled)"
${KUBECTL} -n default patch objects.kubernetes.m.crossplane.io foo-service-owner --type='merge' -p '{"spec":{"forProvider":{"manifest":{"metadata":{"labels":{"unwanted":null}}}}}}'
echo " ✅ Removing a label before switching to SSA."

# ensure the MR gets reconciled (SSA-disabled)
MR_GENERATION=$(${KUBECTL} -n default get objects.kubernetes.m.crossplane.io foo-service-owner -o jsonpath='{.metadata.generation}')
kubectl wait --for=jsonpath='{.status.conditions[?(@.type=="Synced")].observedGeneration}'="$MR_GENERATION" objects.kubernetes.m.crossplane.io foo-service-owner --timeout=10s

# Validate that provider fails to remove the "unwanted" label from the target k8s object
# (SSA-disabled)
echo " ⏳ Validating unwanted label still exists at the target object..."
unwanted_value=$(${KUBECTL} -n default get service foo-service -o jsonpath='{.metadata.labels.unwanted}')
if [ -z "$unwanted_value" ]; then
  echo " $unwanted_value "
  exit 1
fi
current_labels=$(${KUBECTL} -n default get service foo-service -o jsonpath='{.metadata.labels}')
echo " 🏷️ Current labels: $current_labels"
current_managefields=$(${KUBECTL} -n default get service foo-service -o jsonpath='{.metadata.managedFields}')
echo " 🔎 Current managed fields before SSA: $current_managefields"
echo " ✅ Validated that the unwanted label still exists at the target resource"
# We reached to our desired scenario at at https://github.com/kubernetes/kubernetes/issues/99003
# - `metadata.labels.unwanted` is not in MR's desired manifest
# - The target k8s resource still has `metadata.labels.unwanted` with legacy CSA field manager
# - Plain SSA would leave `metadata.labels.unwanted` with a orphan field manager
# - We expect the provider to transfer csa field managers to ssa field manager first, so that it is not orphan

# Enable server-side apply
# The loop ensures that the provider pod rollout is completed
echo " ⏳ Enabling server-side apply in provider..."
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug", "--enable-server-side-apply"]}]'
echo " ⏳ Waiting for the provider to become ready..."

START_TIME=$(date +%s)
TIMEOUT_SECONDS=15
while true; do
  NOW=$(date +%s)
  ELAPSED=$((NOW - START_TIME))

  if (( ELAPSED >= TIMEOUT_SECONDS )); then
    echo " ❌ Timeout after ${TIMEOUT_SECONDS}s. The provider could not enable SSA"
    exit 1
  fi
  if ${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes \
      -o jsonpath='{range .items[*]}{.spec.containers[?(@.name=="package-runtime")].args[*]}{"\n"}{end}'\
      | grep -qv -- '--enable-server-side-apply'; then
    echo " ⏳ Waiting for the provider to become ready..."
    sleep 1
  else
    echo " ☑️ Provider SSA enablement completed."
    ${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes \
          -o jsonpath='{range .items[*]}{.spec.containers[?(@.name=="package-runtime")].args[*]}{"\n"}{end}'
    break
  fi
done
current_provider_args=$(${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes -o jsonpath='{range .items[*]}{.spec.containers[?(@.name=="package-runtime")].args[*]}{"\n"}{end}')
echo " ⛭ current provider args: $current_provider_args"
echo " ☑️ Enabled server-side apply in provider..."

# We have enabled the server-side apply
# Validate last applied annotation is removed from the target object, as part of the SSA migration
last_applied_annotation=$(${KUBECTL} -n default get service foo-service -o jsonpath="{.metadata.annotations['kubectl.kubernetes.io/last-applied-configuration']}")
if [ -n "$last_applied_annotation" ]; then # This annotation should not be present when SSA is enabled
  echo "❌SSA migration validation failed! Annotation 'last-applied-configuration' should not exist when SSA is enabled!"
  exit 1
fi

# Validate that the unwanted label correctly removed.
unwanted_value=$(${KUBECTL} -n default get service foo-service -o jsonpath='{.metadata.labels.unwanted}')
if [ -n "$unwanted_value" ]; then
  echo "❌SSA migration validation failed! Label 'unwanted' still exists with value $unwanted_value on target object"
  exit 1
fi
current_labels=$(${KUBECTL} -n default get service foo-service -o jsonpath='{.metadata.labels}')
echo " 🏷️ Labels after SSA: $current_labels"
echo " ✅ Validated that SSA migration correctly removed the unwanted label!"
current_managefields=$(${KUBECTL} -n default get service foo-service -o jsonpath='{.metadata.managedFields}')
echo " 🔎 Current managed fields after SSA: $current_managefields"
echo " ✅ Successfully validated the SSA migration!"

