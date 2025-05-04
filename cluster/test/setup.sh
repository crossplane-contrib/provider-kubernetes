#!/usr/bin/env bash
set -aeuo pipefail

echo "Running setup.sh"

echo "Creating the provider config with cluster admin permissions in cluster..."
SA=$(${KUBECTL} -n crossplane-system get sa -o name | grep provider-kubernetes | sed -e 's|serviceaccount\/|crossplane-system:|g')
${KUBECTL} create clusterrolebinding provider-kubernetes-admin-binding --clusterrole cluster-admin --serviceaccount="${SA}" --dry-run=client -o yaml | ${KUBECTL} apply -f -

cat <<EOF | ${KUBECTL} apply -f -
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: kubernetes-provider
spec:
  credentials:
    source: InjectedIdentity
EOF

if [ "${E2E_SSA_ENABLED:-false}" == "true" ]; then
  echo "Enabling ssa feature for the provider"
  ${KUBECTL} patch deploymentruntimeconfig runtimeconfig-provider-kubernetes --type='json' \
  -p='[{"op":"replace","path":"/spec/deploymentTemplate/spec/template/spec/containers/0/args", "value":["--debug", "--enable-server-side-apply"]}]'
  PROVIDER_DEPLOYMENT_NAME="$(${KUBECTL} -n crossplane-system get deployment -o name | grep provider-kubernetes)"
  ${KUBECTL} -n crossplane-system wait --for=jsonpath='{.spec.template.spec.containers[0].args[?(@=="--enable-server-side-apply")]}' "$PROVIDER_DEPLOYMENT_NAME"
  ${KUBECTL} -n crossplane-system rollout status "$PROVIDER_DEPLOYMENT_NAME"
  ${KUBECTL} -n crossplane-system wait --for=jsonpath='{.status.replicas}'="1" "$PROVIDER_DEPLOYMENT_NAME"
  ${KUBECTL} -n crossplane-system get pods
fi