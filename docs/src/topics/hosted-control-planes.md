# Hosted control planes

CAPSTK can provision worker machines for an externally hosted control plane.
The hosting controller supplies the Cluster API `Cluster`, `Machine` resources,
and bootstrap Secrets. CAPSTK uses the Cluster API **v1beta2** contract; the
STACKIT infrastructure resources retain their **v1alpha1** API version.

## Run a manager per namespace

Use `--namespace` to limit every controller cache and watch, including Secrets,
to the hosted cluster's management namespace. Run the manager with a service
account whose Role and RoleBinding grant access only in that namespace. Cache
scoping complements Kubernetes RBAC; it does not replace RBAC.

```yaml
containers:
- name: manager
  image: <your-capstk-image>
  args:
  - --namespace=$(POD_NAMESPACE)
  - --leader-elect
  - --health-probe-bind-address=:8081
  - --metrics-bind-address=0
  env:
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: ENABLE_WEBHOOKS
    value: "false"
```

With `--namespace` set, leader election leases default to that namespace. Use
`--leader-election-namespace` to override the lease namespace if needed. The
health and readiness endpoints are `/healthz` and `/readyz` on the configured
probe address. Without `--namespace`, existing cluster-wide behavior remains.

Disabling webhooks prevents this manager from starting an admission server or
requiring serving certificates. Install the CRDs separately. Admission webhook
configurations, if installed, must still point to a running admission server;
do not point them at a manager with `ENABLE_WEBHOOKS=false`.

Credentials and bootstrap Secrets must be in the watched namespace. Set
`StackitCluster.spec.credentialsSecretRef.namespace` to that namespace or omit
it. The credentials Secret requires `serviceaccount.json`; its optional
`project-id` key must match `StackitCluster.spec.projectID` when supplied.

## Reuse the hosted API endpoint

Configure a `StackitCluster` with the existing worker network, hosted API
endpoint, and `spec.apiServerLoadBalancer.enabled: false`. Leave
`spec.bastion.enabled` false when no CAPSTK-managed bastion is required. CAPSTK
validates the network and credentials and publishes infrastructure readiness
without creating an API server load balancer or control-plane machines.

An external infrastructure controller can instead own the `StackitCluster`
lifecycle using the `cluster.x-k8s.io/managed-by` annotation. In that case CAPSTK
does not change the cluster, including its finalizers or status, or manage its
cloud resources. The external controller must populate `status.ready` and
`status.initialization.provisioned` for worker provisioning and CAPI readiness,
and keep the cluster and credentials available until its machines are deleted.
Do not annotate an existing CAPSTK-managed cluster without also transferring
responsibility for its cloud resources and finalizers.

## Bootstrap worker machines

Point each `Machine.spec.bootstrap.dataSecretName` at a Secret containing the
bootstrap document in `data.value` (or `data.userData` for legacy consumers).
CAPSTK treats the bytes as opaque: cloud-init and Ignition JSON are forwarded
unchanged, then base64-encoded once for the STACKIT API. CAPSTK does not render,
merge, or inject configuration into the document.

Select an image that supports the bootstrap format and STACKIT's metadata
service or config drive. A native `stackit` CoreOS image also needs the STACKIT
providers in Ignition and Afterburn; changing the image's platform ID does not
add those providers. Track image availability in the
[Fedora CoreOS platform request](https://github.com/coreos/fedora-coreos-tracker/issues/2175).
The control-plane integration remains responsible for generating the matching
bootstrap data, configuring the node's cloud provider and networking, and
validating the chosen image with an end-to-end worker lifecycle test.
