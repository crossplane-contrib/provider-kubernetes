# ProviderConfig

A `ProviderConfig` tells provider-kubernetes how to connect to and authenticate
with a target cluster. It has two independent parts:

- **`spec.credentials`** — how to reach the cluster (server URL and CA). Usually
  a kubeconfig stored in a Secret, or `InjectedIdentity` when the target is the
  same cluster the provider runs in.
- **`spec.identity`** — how to authenticate (bearer token source). Optional;
  omit it to use the credentials from the kubeconfig directly.

## Credentials sources

| `source` | Description |
|---|---|
| `Secret` | Kubeconfig read from a Secret key. |
| `InjectedIdentity` | In-cluster config; targets the cluster the provider pod runs in. |
| `Environment` | Kubeconfig read from an environment variable. |
| `Filesystem` | Kubeconfig read from a file path. |

## Identity types

When `spec.identity` is set, the provider overlays its token source on top of
the kubeconfig auth. The kubeconfig still supplies the server URL and CA; the
identity supplies the bearer token.

| `type` | Description |
|---|---|
| `GoogleApplicationCredentials` | OAuth2 token from a GCP service account key or Workload Identity. |
| `AzureServicePrincipalCredentials` | Token from an Azure service principal. |
| `AzureWorkloadIdentityCredentials` | Token from Azure Workload Identity. |
| `AWSWebIdentityCredentials` | SigV4 presigned URL token via IRSA or Web Identity. |
| `UpboundTokens` | Token from an Upbound robot or session token. |
| `NebiusServiceAccountCredentials` | IAM token from a Nebius service account key. |
| `ServiceAccount` | Short-lived token minted via the Kubernetes TokenRequest API. |

---

## ServiceAccount identity

Use `type: ServiceAccount` when the target cluster is the same cluster the
provider runs in and you want to scope the provider's access to a specific
ServiceAccount's RBAC — without storing a long-lived kubeconfig or bearer token
in a Secret.

The provider calls the Kubernetes TokenRequest API using its own in-cluster
credentials to mint a short-lived token for the referenced ServiceAccount, then
uses that token as the bearer credential for all calls to the target cluster.
Tokens are cached and refreshed automatically before they expire; no Secret
rotation is needed.

### ProviderConfig

`spec.credentials.source` must be `InjectedIdentity` — the provider's own
in-cluster config is the only viable server+CA source when target and provider
cluster are the same.

```yaml
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: team-a
spec:
  credentials:
    source: InjectedIdentity
  identity:
    type: ServiceAccount
    source: InjectedIdentity
    serviceAccountRef:
      name: deployer        # ServiceAccount to mint tokens for
      namespace: team-a     # Namespace that ServiceAccount lives in
      audiences:            # Optional — see note on audiences below
        - kubernetes
      expirationSeconds: 3600  # Optional — defaults to 3600; the API server
                               # may return a shorter or longer lifetime
```

### RBAC prerequisites

**1. Allow the provider to mint tokens.**

The provider's own ServiceAccount needs `create` on
`serviceaccounts/token` for the target ServiceAccount. Grant this with a
Role in the target namespace (adjust `crossplane-system` and
`provider-kubernetes` to match your installation):

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: tokenrequest-deployer
  namespace: team-a
rules:
  - apiGroups: [""]
    resources: ["serviceaccounts/token"]
    resourceNames: ["deployer"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tokenrequest-deployer
  namespace: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: tokenrequest-deployer
subjects:
  - kind: ServiceAccount
    name: provider-kubernetes
    namespace: crossplane-system
```

**2. Grant the target ServiceAccount the permissions it needs.**

The TokenRequest permission only allows the provider to mint the token. The
target ServiceAccount must also have the RBAC permissions needed for the
resources that the provider will manage on its behalf. For example, to let
`deployer` manage ConfigMaps in `team-a`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: deployer
  namespace: team-a
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: deployer
  namespace: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: deployer
subjects:
  - kind: ServiceAccount
    name: deployer
    namespace: team-a
```

### Note on audiences

The `audiences` field sets the `spec.audiences` of the TokenRequest. The
value must be accepted by the target cluster's API server. If omitted, the
API server's default audience is used.

The default varies by distribution:

| Distribution | Default audience |
|---|---|
| kubeadm / kind | `https://kubernetes.default.svc.cluster.local` |
| Gardener shoots | `kubernetes` |
| EKS | cluster ARN |

Check what your cluster mints by decoding a test token:

```sh
kubectl create token default -n default --duration=1h \
  | cut -d. -f2 | base64 -d 2>/dev/null | python3 -m json.tool | grep aud
```

If you set `audiences` explicitly, use the exact string your cluster accepts.
A mismatch causes authentication failures with no clear error from the API
server.
