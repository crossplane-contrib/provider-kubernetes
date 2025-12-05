#!/usr/bin/env bash
set -aeuo pipefail

echo " ⏳ Disabling SSA feature for the provider..."
${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug", "--no-enable-server-side-apply"]}]'

# Ensure a rollout is completed
LABEL="pkg.crossplane.io/provider=provider-kubernetes"
CONTAINER="package-runtime"
EXPECTED_ARG="--no-enable-server-side-apply"
TIMEOUT_SECONDS=15
SLEEP_INTERVAL=1

echo " ⏳ Waiting up to ${TIMEOUT_SECONDS}s for all pods with label '$LABEL'..."
echo " 📜 Conditions:"
echo "  - container '$CONTAINER' exists"
echo "  - container is Running"
echo "  - pod Ready=True"
echo "  - container args include '$EXPECTED_ARG'"
echo

START_TIME=$(date +%s)

while true; do
  NOW=$(date +%s)
  ELAPSED=$((NOW - START_TIME))

  if (( ELAPSED >= TIMEOUT_SECONDS )); then
    echo "❌ Timeout after ${TIMEOUT_SECONDS}s. Condition not met."
    exit 1
  fi

  PODS_JSON=$(${KUBECTL} get pods -A -l "$LABEL" -o json)
  POD_NAMES=$(echo "$PODS_JSON" | jq -r '.items[].metadata.name')

  if [[ -z "$POD_NAMES" ]]; then
    echo "No provider pods found yet..."
    sleep "$SLEEP_INTERVAL"
    continue
  fi

  ALL_READY=true

  for POD in $POD_NAMES; do
    POD_JSON=$(echo "$PODS_JSON" | jq --arg pod "$POD" '.items[] | select(.metadata.name==$pod)')

    # 1) Check pod Ready condition
    POD_READY=$(echo "$POD_JSON" \
      | jq -r '.status.conditions[]? | select(.type=="Ready") | .status')

    if [[ "$POD_READY" != "True" ]]; then
      echo "Provider pod $POD is not Ready yet..."
      ALL_READY=false
      break
    fi

    # 2) Check container state is Running
    CONTAINER_RUNNING=$(echo "$POD_JSON" \
      | jq --arg c "$CONTAINER" '
          .status.containerStatuses[]
          | select(.name==$c)
          | .state.running != null
        ')

    if [[ "$CONTAINER_RUNNING" != "true" ]]; then
      echo "Provider pod $POD container '$CONTAINER' is not Running..."
      ALL_READY=false
      break
    fi

    # 3) Check container args contain expected arg
    HAS_ARG=$(echo "$POD_JSON" \
      | jq --arg c "$CONTAINER" --arg arg "$EXPECTED_ARG" '
          .spec.containers[]
          | select(.name==$c)
          | (.args // [])
          | any(. == $arg)
        ')

    if [[ "$HAS_ARG" != "true" ]]; then
      echo "Pod $POD container '$CONTAINER' missing arg '$EXPECTED_ARG'..."
      ALL_READY=false
      break
    fi
  done

  if [[ "$ALL_READY" == "true" ]]; then
    echo "✅ All pods satisfy: Running, Ready, and contain required arg."
    break
  fi

  sleep "$SLEEP_INTERVAL"
done

current_provider_args=$(${KUBECTL} get pods -A -l pkg.crossplane.io/provider=provider-kubernetes -o jsonpath='{range .items[*]}{.spec.containers[?(@.name=="package-runtime")].args[*]}{"\n"}{end}')
echo " ⚙️ Current provider args: $current_provider_args"
echo " ☑️ SSA is disabled successfully"

echo " ▶️ Unpausing the MR"
${KUBECTL} annotate objects.kubernetes.crossplane.io foo-service-owner 'crossplane.io/paused=false' --overwrite
sleep 1