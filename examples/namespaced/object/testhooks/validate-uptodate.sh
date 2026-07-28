#!/usr/bin/env bash
set -aeuo pipefail

# Validates the UpToDate condition for an observe-only Object, triggered by the
# uptest framework via `uptest.upbound.io/post-assert-hook`:
# https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks
#
# Scenario:
#   1. The Object observes a ConfigMap and desires data.sample-key=sample-value.
#      The seed ConfigMap matches, so UpToDate starts True (ObserveMatched).
#   2. We patch the LIVE ConfigMap so data.sample-key diverges from desired.
#   3. UpToDate must flip to False (UpdateRestricted) with a non-empty message
#      carrying the diff - all while the resource is observe-only, so the
#      ConfigMap must NOT be reverted by the provider.
KUBECTL="kubectl"
NS="default"
OBJECT="observe-uptodate"
CONFIGMAP="observe-uptodate"

# get_uptodate <jsonpath-suffix> prints a field of the UpToDate condition.
get_uptodate() {
  ${KUBECTL} get object.kubernetes.m.crossplane.io "${OBJECT}" -n "${NS}" \
    -o jsonpath="{.status.conditions[?(@.type=='UpToDate')]$1}"
}

# wait_uptodate <status> <reason> waits up to ~60s for the UpToDate condition to
# reach the given status/reason, failing otherwise.
wait_uptodate() {
  local want_status="$1" want_reason="$2" i status reason
  for i in $(seq 1 30); do
    status="$(get_uptodate '.status')"
    reason="$(get_uptodate '.reason')"
    if [ "${status}" == "${want_status}" ] && [ "${reason}" == "${want_reason}" ]; then
      echo "UpToDate is ${status}/${reason} as expected"
      return 0
    fi
    echo "waiting for UpToDate=${want_status}/${want_reason} (currently ${status:-<none>}/${reason:-<none>})..."
    sleep 2
  done
  echo "Timed out waiting for UpToDate=${want_status}/${want_reason}"
  echo "Object status conditions:"
  ${KUBECTL} get object.kubernetes.m.crossplane.io "${OBJECT}" -n "${NS}" -o jsonpath='{.status.conditions}' | tr ',' '\n'
  return 1
}

echo "Step 1: expect UpToDate=True (ObserveMatched) - observed matches desired"
wait_uptodate "True" "ObserveMatched"

echo "Step 2: patch the LIVE ConfigMap so it diverges from the desired manifest"
${KUBECTL} patch configmap "${CONFIGMAP}" -n "${NS}" \
  --type='merge' -p='{"data":{"sample-key":"drifted-value"}}'

echo "Step 3: expect UpToDate=False (UpdateRestricted) - drift surfaced, no update"
wait_uptodate "False" "UpdateRestricted"

echo "Step 3a: the condition message must carry a (non-empty) diff"
MESSAGE="$(get_uptodate '.message')"
if [ -z "${MESSAGE}" ]; then
  echo "Expected a non-empty UpToDate message containing the diff, got empty"
  exit 1
fi
echo "UpToDate message (diff):"
echo "${MESSAGE}"

echo "Step 4: confirm the ConfigMap was NOT reverted (observe-only makes no changes)"
LIVE_VALUE="$(${KUBECTL} get configmap "${CONFIGMAP}" -n "${NS}" -o jsonpath='{.data.sample-key}')"
if [ "${LIVE_VALUE}" != "drifted-value" ]; then
  echo "Expected live ConfigMap sample-key to remain 'drifted-value' (unchanged by the provider), got '${LIVE_VALUE}'"
  exit 1
fi
echo "ConfigMap still holds 'drifted-value' - observe-only did not revert it"

echo "Successfully validated the UpToDate condition for observe-only drift!"
