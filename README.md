# namespace-class

A Kubernetes controller that manages cluster-wide resource policies via `NamespaceClass` custom resources.
When a namespace is labeled with a NamespaceClass, the controller automatically applies (and keeps applied) the
resources defined in that NamespaceClass — such as ConfigMaps, Secrets, NetworkPolicies, or any other Kubernetes
object.

## How it works

### NamespaceClass

A cluster-scoped custom resource that defines a set of manifests to be *applied* to any namespace that references it.

```yaml
apiVersion: qiujian16.github.com.qiujian16.github.com/v1
kind: NamespaceClass
metadata:
  name: my-policies
spec:
  policies:
    manifests:
      # A ConfigMap with default data
      - apiVersion: v1
        kind: ConfigMap
        metadata:
          name: app-config
        data:
          env: production
      # A NetworkPolicy
      - apiVersion: networking.k8s.io/v1
        kind: NetworkPolicy
        metadata:
          name: deny-all
        spec:
          podSelector: {}
          policyTypes:
            - Ingress
```

### Namespace

To associate a namespace with a NamespaceClass, add the label `namespaceclass.akuity.io/name`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: my-app
  labels:
    namespaceclass.akuity.io/name: my-policies
```

### Create / Update / Delete flows

#### NamespaceClass lifecycle

| Action | Behaviour |
|---|---|
| **Create** | A finalizer is added. The controller watches for namespaces labeled with this NamespaceClass. |
| **Update** | Namespaces referencing this class are re-reconciled — new manifests are applied, removed manifests are deleted. |
| **Delete** | The finalizer blocks deletion while the controller cleans up all applied resources from every namespace that references the class. Once all resources are gone, the finalizer is removed and the NamespaceClass is deleted. |

#### Namespace lifecycle

| Action | Behaviour |
|---|---|
| **Label added** | A finalizer is added to the namespace. All manifests from the referenced NamespaceClass are applied into the namespace (namespace is set automatically). An annotation `namespaceclass.akuity.io/relatedresources` records what was applied. |
| **Label changed** | Resources from the old NamespaceClass are deleted; resources from the new one are applied. The annotation is updated. |
| **Label removed** | All applied resources are cleaned up. The annotation is removed. The finalizer is removed so the namespace can be deleted freely. |
| **Namespace deleted** | The finalizer blocks deletion. The controller cleans up all applied resources, removes the annotation, then removes the finalizer — allowing the namespace to finish deleting. |
| **External modification** | The controller periodically re-checks (every ~5s with jitter). If an applied resource is modified externally, the controller reverts it to match the NamespaceClass spec. |

### Resource diffing

On each reconciliation the controller compares the *desired* resource list (from the NamespaceClass spec) with the
*existing* resource list (from the `namespaceclass.akuity.io/relatedresources` annotation on the namespace):

- **Resources in the spec but not in the annotation** → created.
- **Resources in both** → compared (excluding status, resourceVersion, etc.); updated if different, skipped if equal.
- **Resources in the annotation but not in the spec** → deleted.

This means that removing a manifest from a NamespaceClass automatically removes that resource from every namespace.

### Webhook validation

A validating webhook runs on NamespaceClass create and update:

| Rule | Error |
|---|---|
| Manifest must decode to a valid Kubernetes resource | `manifest[0]: failed to decode` |
| No duplicate apiVersion/kind/name within the same NamespaceClass | `manifest[2] (ConfigMap/cm-a): duplicate resource` |
| Namespace resources are not allowed | `manifest[1]: Namespace resources are not allowed` |

> **Note:** `apiVersion`, `kind`, `metadata.name`, and `metadata.namespace` are already validated at the CRD level
> via the `+kubebuilder:validation:EmbeddedResource` marker, so the controller itself does not set `metadata.namespace`
> in the manifest — it applies the namespace at runtime.

## Future Work

### Server-side apply strategy

The current implementation uses a **client-side update** strategy (Get → compare → Update). This can cause
flip-flops when another controller or user modifies labels or annotations on a managed resource — the
NamespaceClass controller will see a diff and overwrite the change, the other actor changes it back, and
the cycle repeats.

A planned improvement is to let users opt into **server-side apply** (SSA) on a per-NamespaceClass basis.
With SSA the controller declares ownership of specific fields via a field manager, and the API server
merges concurrent changes from different managers without conflict. This way:

- The NamespaceClass controller owns the fields it sets.
- Other controllers can safely add their own labels, annotations, or spec fields without fighting.
- No flip-flops.

The `ResourceApplyStrategy` interface already exists in the codebase; adding SSA means implementing a
`ServerSideApplyStrategy` and wiring it to a field on the NamespaceClass spec.

```go
type ResourceApplyStrategy interface {
    Apply(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured) error
}
```

## Getting Started

### Prerequisites
- go version v1.26+
- docker version 17.03+
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster.

### Deploy on a cluster

```sh
# Build and push the image
make docker-build docker-push IMG=<registry>/namespace-class:tag

# Install CRDs and deploy the controller
make install
make deploy IMG=<registry>/namespace-class:tag
```

### Quick start

```sh
# 1. Create a NamespaceClass
kubectl apply -f - <<EOF
apiVersion: qiujian16.github.com.qiujian16.github.com/v1
kind: NamespaceClass
metadata:
  name: example-policies
spec:
  policies:
    manifests:
      - apiVersion: v1
        kind: ConfigMap
        metadata:
          name: shared-config
        data:
          key1: value1
EOF

# 2. Create a namespace and label it
kubectl create namespace my-app
kubectl label namespace my-app namespaceclass.akuity.io/name=example-policies

# 3. Verify the ConfigMap was applied
kubectl get configmap shared-config -n my-app
```

### Uninstall

```sh
make undeploy
make uninstall
```

## Development

```sh
# Run tests
make test

# Run e2e tests (requires kind and cert-manager)
make test-e2e

# Generate manifests
make manifests

# Generate code
make generate
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
