# Sanitizing secrets in provider-kubernetes

An `Object` can copy a value from a `v1` Secret on the control plane into the manifest
it applies with `spec.references[].patchesFrom`, e.g. to deliver a Secret to another cluster:

```yaml
spec:
  references:
  - patchesFrom:
      apiVersion: v1
      kind: Secret
      name: bar
      namespace: default
      fieldPath: data.password
    toFieldPath: data.password
  forProvider:
    manifest:
      apiVersion: v1
      kind: Secret
      metadata:
        namespace: default
```

By default, the provider patches that value into `.spec.forProvider.manifest` of the `Object`.
It is then stored with the `Object`, readable by anyone who can read `Object`s, and kept
there after the Secret changes. It may also show up in `.status.atProvider.manifest`, in
conditions and in Events.

With the `--sanitize-secrets` flag (or `SANITIZE_SECRETS=true`), which is off by default,
the provider keeps such values out of the `Object`. See
[the example](../examples/namespaced/object/object-sanitize-secrets.yaml).

### What the flag protects

The flag applies to references whose `patchesFrom` sets both `apiVersion: v1` and
`kind: Secret` (they default to the `Object` kind), for cluster-scoped and namespaced
`Object`s, with server-side and client-side apply:

- The value is applied to the target object, but never patched into the `Object`'s spec.
  When the Secret changes, the next reconcile applies the new value.
- In `.status.atProvider.manifest`, the field patched from a Secret, and values of 6
  characters or more read from it wherever else the target holds them (e.g. after a
  mutating admission), are replaced with `"<redacted>"`; the `data` and `stringData` of a
  target `Secret` with `{"redacted": null}`; and the
  `kubectl.kubernetes.io/last-applied-configuration` annotation is removed. The target's
  name, namespace and `managedFields` are kept as they are.
- Values of 6 characters or more are replaced with `<redacted>` in the error messages, which
  end up in conditions and Events, also in their base64-decoded form when they are read
  from the `data` of the Secret.
- The observed objects logged with `--debug` are redacted the same way.

Such a reference can't patch `metadata`, `metadata.name` or `metadata.namespace`: deleting
an `Object` doesn't resolve its references, so these must be stored. The `Object` reports
an error instead.

### What isn't covered

- Values written into `.spec.forProvider.manifest` directly, e.g. by a composition.
- Values from other kinds, such as a `ConfigMap` or another `Object`. Their references
  patch the `Object` as before.
- The target object, which receives the value by design. With client-side apply
  (`--no-enable-server-side-apply`), its `kubectl.kubernetes.io/last-applied-configuration`
  annotation holds the applied manifest, values included. Server-side apply, the default,
  doesn't set it.

### Enabling the flag for existing Objects

The flag doesn't remove values that an `Object` stored before it was enabled. Recreate
these `Object`s, or remove the values from their manifest. Mind that deleting an `Object`
deletes its target object, unless its `managementPolicies` leave out `Delete`. Then rotate
the Secrets, as the values also remain in etcd history, backups and audit logs.

> [!WARNING]
> With the flag, patching from the `.status.atProvider.manifest` of another `Object`
> reads the redacted values. Use `spec.connectionDetails`, or reference the Secret directly.
