#!/usr/bin/env bash
set -aeuo pipefail

# Validates the UpToDate condition for an observe-only Object, triggered by the
# uptest framework via `uptest.upbound.io/post-assert-hook`:
# https://github.com/crossplane/uptest/tree/e64457e2cce153ada54da686c8bf96143f3f6329?tab=readme-ov-file#hooks
#
# Models the import/adoption use case (see observe-uptodate.yaml):
#   1. A Secret exists (labels.app=legacy, data.sample-key=live-value), owned by
#      another actor. The observe-only Object's manifest desires app=adopted and
#      sample-key=desired-value, so an update (were it allowed) would change both
#      fields. UpToDate must be False (UpdateRestricted) with a message naming
#      those changes by key path - the Secret value redacted - and the Secret
#      must NOT be modified (observe-only).
#   2. We reconcile the live Secret to match the manifest. Now an update would
#      change nothing, so UpToDate must flip to True (ObserveMatched) - the
#      "safe to adopt" signal.
#
# Secret data values are base64-encoded; the manifest and patches use:
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

# At this point the observed Secret differs from the manifest in two fields, and
# the Object is observe-only. The UpToDate condition looks like this:
#
#   - type: UpToDate
#     status: "False"
#     reason: UpdateRestricted
#     message: |
#         ~ data.sample-key: <redacted> -> <redacted>
#         ~ metadata.labels.app: legacy -> adopted
#
# The message lists each field an update would change, by its full key path
# (no line numbers, so it maps directly onto a GitOps manifest regardless of key
# ordering). Secret data values are redacted - deliberately, and unlike
# status.atProvider's flag-gated sanitization - because the condition message is
# far more widely visible than an RBAC-gated read of the Secret. Once the live
# Secret is reconciled to match the manifest (Step 2), the condition becomes
# status "True", reason ObserveMatched, with an empty message.
echo "Step 1: expect UpToDate=False (UpdateRestricted) - an update would change fields"
wait_uptodate "False" "UpdateRestricted"

echo "Step 1a: the condition message must name the would-be changes by path"
MESSAGE="$(get_uptodate '.message')"
if [ -z "${MESSAGE}" ]; then
  echo "Expected a non-empty UpToDate message containing the diff, got empty"
  exit 1
fi
echo "UpToDate message (diff):"
echo "${MESSAGE}"
# The non-sensitive label change is shown in full, by path.
if ! echo "${MESSAGE}" | grep -q "metadata.labels.app: legacy -> adopted"; then
  echo "Expected the diff to show the label change 'metadata.labels.app: legacy -> adopted'"
  exit 1
fi
# The Secret data change is shown by path but with its value redacted.
if ! echo "${MESSAGE}" | grep -q "data.sample-key: <redacted>"; then
  echo "Expected the diff to show 'data.sample-key: <redacted>'"
  exit 1
fi
# The actual Secret values must never appear in the condition message.
if echo "${MESSAGE}" | grep -qE "${LIVE_B64}|${DESIRED_B64}"; then
  echo "Secret value leaked into the UpToDate message; it must be redacted"
  exit 1
fi

echo "Step 1b: confirm the Secret was NOT modified (observe-only makes no changes)"
LIVE_VALUE="$(${KUBECTL} get secret "${SECRET}" -n "${NS}" -o jsonpath='{.data.sample-key}')"
LIVE_LABEL="$(${KUBECTL} get secret "${SECRET}" -n "${NS}" -o jsonpath='{.metadata.labels.app}')"
if [ "${LIVE_VALUE}" != "${LIVE_B64}" ] || [ "${LIVE_LABEL}" != "legacy" ]; then
  echo "Expected live Secret unchanged (sample-key=${LIVE_B64}, labels.app=legacy), got sample-key='${LIVE_VALUE}', labels.app='${LIVE_LABEL}'"
  exit 1
fi
echo "Secret still holds its original values - observe-only did not modify it"

echo "Step 2: reconcile the live Secret to match the manifest (both fields)"
${KUBECTL} patch secret "${SECRET}" -n "${NS}" \
  --type='merge' -p="{\"metadata\":{\"labels\":{\"app\":\"adopted\"}},\"data\":{\"sample-key\":\"${DESIRED_B64}\"}}"

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
