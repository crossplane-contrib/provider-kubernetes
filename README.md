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
kubectl crossplane install provider crossplane/provider-kubernetes:main
```

You may also manually install `provider-kubernetes` by creating a `Provider` directly:

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubernetes
spec:
  package: "crossplane/provider-kubernetes:main"
```

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
      kubectl apply -f examples/provider/config-in-cluster.yaml
      ```
  1. If provider kubernetes running outside the cluster (e.g. running locally with `make run`)

      ```
      KUBECONFIG=$(kind get kubeconfig --name local-dev | sed -e 's|server:\s*.*$|server: http://localhost:8081|g')
      kubectl -n crossplane-system create secret generic cluster-config --from-literal=kubeconfig="${KUBECONFIG}"
      kubectl apply -f examples/provider/config.yaml
      ```

1. Now you can create `Object` resources with provider reference, see [sample object.yaml](examples/object/object.yaml).

    ```
    kubectl create -f examples/object/object.yaml
    ```

### Cleanup

To delete the local kind cluster:

```
make controlplane.down
```
