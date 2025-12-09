# Server-side apply in provider-kubernetes

For managing and syncing k8s resources, provider-kubernetes uses server-side apply (SSA)
by default.
See [official kubernetes documentation](https://kubernetes.io/docs/reference/using-api/server-side-apply/)
for reference on how SSA works in Kubernetes.

### General behavior of SSA

The `Object` managed resources (MRs) in `kubernetes.crossplane.io/v1alpha1`(cluster-scoped)
and `kubernetes.m.crossplane.io/v1alpha1`(namespace-scoped) set the fully specified intent
for the desired k8s object in their `.spec.forProvider.manifest`.  

For a given `Object` MR, `provider-kubernetes` uses the field manager `provider-kubernetes/object-mr-name`
while applying the desired k8s resource with SSA. The provider syncs and detect drifts only for the fields
it manages, that are specified in the `.spec.forProvider.manifest` in accordance with K8s SSA mechanics.

Per [k8s recommendation for controllers](https://kubernetes.io/docs/reference/using-api/server-side-apply/#using-server-side-apply-in-a-controller)
,in case of conflicts on field management `provider-kubernetes` forces the conflicts.

> [!WARN]
> Care should be taken if there are other external controllers managing the same k8s resource.
> They should not manage the same fields, as they might race on the field ownerships.


### Switching from patch-based syncer to SSA

For existing `Object` MRs, provider-kubernetes automatically handles the switch from
legacy patch-based syncer to server-side apply based syncer. In the desired k8s resource,
fields that are previously managed by the provider-kubernetes patch-based syncer are 
transferred to the SSA-based syncer's field manager. 

> [!INFO]
> `.metadata.managedFields` entries belonging to other external
> managers are not modified during this switch.

### Managing the same k8s resource with multiple Object MRs

While it is discouraged, with SSA, it is possible that multiple `Object` MRs can
target the same k8s resource and manage different fields of that k8s resource.
Also, an `Object` MR may reference an existing k8s object that was created/managed
by some external actor/operator and partially manage that k8s resource.

#### Caveats:

Each `Object` MR must specify disjoint field sets at their `.spec.forProvider.manifest`
for the target resource, i.e. they must manage different fields. Otherwise, they will
race on fields.

In this case, some manifests might be partial. They are not valid standalone manifests,
and can't be created, but they are valid patches to an existing k8s resource.
- One of the `Object` MRs (or any other existing k8s resource) should become the main "lead"
for the full lifecycle. It should specify a valid resource suitable for creation.
- Those resources should set `spec.managementPolicies` as `["Observe", "Update"]` only.

This way, `Object` MRs with partial manifest will not be able to apply until the target
k8s resource exists. They will stay at `Synced: False` state.
Also, during deletion of these `Object` MRs, they will not delete the target k8s
resource. As a current limitation, they will not remove the partial fields they specify.
