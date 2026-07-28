#!/usr/bin/env bash
set -aeuo pipefail

# Validates the UpToDate condition for an observe-only Object, triggered by the
# uptest framework via `uptest.upbound.io/post-assert-hook`:
# https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks
#
# Models the import/adoption use case (see observe-uptodate.yaml):
#   1. A Secret exists with sample-key=live-value, owned by another actor. The
#      observe-only Object's manifest desires sample-key=desired-value, so an
#      update (were it allowed) would change that field. UpToDate must be False
#      (UpdateRestricted) with a message naming the change - and the Secret must
#      NOT be modified (observe-only).
#   2. We reconcile the live Secret to sample-key=desired-value. Now an update
#      would change nothing, so UpToDate must flip to True (ObserveMatched) -
#      the "safe to adopt" signal.
#
# Secret values are base64-encoded in the object (and therefore in the diff),
# so assertions compare against the base64 encodings:
#   live-value    -> bGl2ZS12YWx1ZQ==
#   desired-value -> ZGVzaXJlZC12YWx1ZQ==
KUBECTL="kubectl"
NS="default"
OBJECT="observe-uptodate"
SECRET="observe-uptodate"
LIVE_B64="bGl2ZS12YWx1ZQ=="
DESIRED_B64="ZGVzaXJlZC12YWx1ZQ=="

# get_uptodate <jsonpath-suffix> prints a field of the UpToDate condition.
get_uptodate() {
  ${KUBECTL} get object.kubernetes.m.crossplane.io "${OBJECT}" -n "${NS}" \
    -o jsonpath="{.status.conditions[?(@.type=='UpToDate')]$1}"
}

# wait_uptodate <status> <reason> waits up to ~150s for the UpToDate condition
# to reach the given status/reason, failing otherwise. The budget comfortably
# exceeds the provider's default 1m poll interval, since an observe-only Object
# only re-observes external changes on its poll cycle.
wait_uptodate() {
  local want_status="$1" want_reason="$2" i status reason
  for i in $(seq 1 75); do
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

echo "Step 1: expect UpToDate=False (UpdateRestricted) - an update would change sample-key"
wait_uptodate "False" "UpdateRestricted"

echo "Step 1a: the condition message must name the would-be change (base64 of desired-value)"
MESSAGE="$(get_uptodate '.message')"
if [ -z "${MESSAGE}" ]; then
  echo "Expected a non-empty UpToDate message containing the diff, got empty"
  exit 1
fi
echo "UpToDate message (diff):"
echo "${MESSAGE}"
if ! echo "${MESSAGE}" | grep -q "${DESIRED_B64}"; then
  echo "Expected the diff to mention the desired value (base64 ${DESIRED_B64})"
  exit 1
fi

echo "Step 1b: confirm the Secret was NOT modified (observe-only makes no changes)"
LIVE_VALUE="$(${KUBECTL} get secret "${SECRET}" -n "${NS}" -o jsonpath='{.data.sample-key}')"
if [ "${LIVE_VALUE}" != "${LIVE_B64}" ]; then
  echo "Expected live Secret sample-key to remain ${LIVE_B64} (live-value), unchanged by the provider, got '${LIVE_VALUE}'"
  exit 1
fi
echo "Secret still holds live-value - observe-only did not modify it"

echo "Step 2: reconcile the live Secret to the desired value (base64 of desired-value)"
${KUBECTL} patch secret "${SECRET}" -n "${NS}" \
  --type='merge' -p="{\"data\":{\"sample-key\":\"${DESIRED_B64}\"}}"

# An observe-only Object only re-observes on its poll cycle; nothing watches the
# external Secret. Nudge an immediate reconcile (as a GitOps controller would by
# touching the resource) so the transition is observed promptly and
# deterministically rather than racing the poll interval.
echo "Step 2a: trigger a reconcile so the change is observed promptly"
${KUBECTL} annotate object.kubernetes.m.crossplane.io "${OBJECT}" -n "${NS}" \
  "test.crossplane.io/reconcile-ts=$(date +%s)" --overwrite

echo "Step 3: expect UpToDate=True (ObserveMatched) - an update would now change nothing"
wait_uptodate "True" "ObserveMatched"

echo "Successfully validated the UpToDate condition for the import/adoption use case!"
