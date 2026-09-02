# provider-kubernetes

`provider-kubernetes` is a Crossplane Provider that enables deployment and management
of arbitrary Kubernetes objects on clusters typically provisioned by Crossplane:

- A `Provider` resource type that only points to a credentials `Secret`.
- An `Object` resource type that is to manage Kubernetes Objects.
- A managed resource controller that reconciles `Object` typed resources and manages arbitrary Kubernetes Objects.

## Install

If you would like to install `provider-kubernetes` without modifications, you may do
so using the Crossplane CLI in a Kubernetes cluster where Crossplane is
installed:

```console
crossplane xpkg install provider xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:v1.0.0
```

You may also manually install `provider-kubernetes` by creating a `Provider` directly:

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubernetes
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:v1.0.0
```

## Target cluster clients

The provider builds one client per distinct ProviderConfig credential set
(kubeconfig plus identity) and keeps it in an LRU cache bounded by
`--client-cache-size` (default 8, `0` removes the bound). Building a client is
expensive because its REST mapper primes itself with full aggregated discovery
on first use, so size the cache to the number of distinct credential sets the
provider talks to.

Client-side rate limiting is disabled for these clients. A cached client is
shared by every concurrent reconcile of its credential set, so client-go's
per-client token bucket (5 QPS, burst 10) would serialize them. This applies to
everything built from the same configuration: the reconcile client, the watch
informers and the server-side apply discovery client. Load on the target
cluster is bounded by `--max-reconcile-rate` on the provider side and by API
Priority and Fairness on the API server.

## Developing locally

See the header of [`go.mod`](./go.mod) for the minimum supported version of Go.

Start a local development environment with Kind where `crossplane` is installed:

```
make
make local-dev
```

Now you can either run the controller locally or in-cluster.

### Running locally

Run controller locally against the cluster:

```
make run
```

Since the controller is running outside the Kind cluster, you need to make the
API server accessible to the controller. You can do this by running a proxy:

```
# on a separate terminal
sudo kubectl proxy --port=8081
```

See [below](#required-configuration) for how to properly setup the RBAC for the
locally running controller.

### Running in-cluster

Run controller in-cluster:

```
make local-deploy
```

See [below](#required-configuration) for how to properly setup the RBAC for the
locally running controller.

### Required configuration

1. Prepare provider config for the local cluster:
  1. If provider kubernetes running in the cluster (e.g. provider installed with crossplane or using `make local-deploy`):

      ```
      SA=$(kubectl -n crossplane-system get sa -o name | grep provider-kubernetes | sed -e 's|serviceaccount\/|crossplane-system:|g')
      kubectl create clusterrolebinding provider-kubernetes-admin-binding --clusterrole cluster-admin --serviceaccount="${SA}"
      kubectl apply -f examples/namespaced/provider/config-in-cluster.yaml
      ```
  1. If provider kubernetes running outside the cluster (e.g. running locally with `make run`)

      ```
      KUBECONFIG=$(kind get kubeconfig --name local-dev | sed -e 's|server:\s*.*$|server: http://localhost:8081|g')
      kubectl -n crossplane-system create secret generic cluster-config --from-literal=kubeconfig="${KUBECONFIG}"
      kubectl apply -f examples/namespaced/provider/config.yaml
      ```

1. Now you can create `Object` resources with provider reference, see [sample object.yaml](examples/namespaced/object/object.yaml).

    ```
    kubectl create -f examples/namespaced/object/object.yaml
    ```

### Cleanup

To delete the local kind cluster:

```
make controlplane.down
```
