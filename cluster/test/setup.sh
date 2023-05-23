#!/usr/bin/env bash
set -aeuo pipefail

echo "Running setup.sh"

echo "Creating the provider config with cluster admin permissions in cluster..."
SA=$(${KUBECTL} -n upbound-system get sa -o name | grep provider-kubernetes | sed -e 's|serviceaccount\/|upbound-system:|g')
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