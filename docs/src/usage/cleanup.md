# Cleanup

Delete a workload cluster through Cluster API:

```sh
kubectl delete cluster "${CLUSTER_NAME}" -n "${NAMESPACE}"
```

Then check Kubernetes resources:

```sh
kubectl get machine,stackitmachine,stackitcluster \
  -n "${NAMESPACE}" \
  -l "cluster.x-k8s.io/cluster-name=${CLUSTER_NAME}"
```

Keep the credentials Secret until all `StackitMachine` and `StackitCluster`
resources have finished deleting. Missing or invalid credentials leave these
resources in `Terminating` with their finalizers intact and a
`CredentialsReady=False` condition. The controllers retry cleanup once valid
credentials are restored; they do not abandon running cloud resources by
removing finalizers after a credential error.

Delete the Cluster before deleting its management namespace. Kubernetes does
not allow creating a replacement Secret in a terminating namespace, so restore
credentials before namespace deletion. If recovery requires manual cleanup,
verify that all provider-owned resources have been removed before explicitly
removing their finalizers. Existing networks supplied to CAPSTK are user-owned
and are preserved during cluster deletion.

Real e2e resources are labeled so they can be cleaned up directly through the
STACKIT API if Kubernetes cleanup fails:

- `cluster-api-provider-stackit/e2e=true`
- `cluster-api-provider-stackit/test-id=<unique-id>`
- `cluster.x-k8s.io/cluster-name=<cluster-name>`
- `cluster.x-k8s.io/cluster-namespace=<namespace>`

Run direct cloud cleanup for tagged e2e resources:

```sh
make cleanup-stackit
```

The cleanup path must not depend on Kubernetes objects. It should find and
delete resources directly by STACKIT labels.
