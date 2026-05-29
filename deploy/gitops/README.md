# UBAG GitOps

Declarative, pull-based delivery of the UBAG Helm chart (`deploy/helm/ubag`)
with **Argo CD** or **Flux**. Both point at the chart in this repository.

## Files

```
deploy/gitops/
├── argocd/
│   ├── application.yaml         # single-cluster Argo CD Application
│   └── applicationset.yaml      # one Application per region (multi-region)
├── flux/
│   ├── gitrepository.yaml       # Flux Git source
│   └── helmrelease.yaml         # Flux HelmRelease from the Git source
└── README.md
```

## Workflow

1. **Fork / set the repo URL.** Replace `repoURL` (Argo CD) and `url`
   (Flux `GitRepository`) with your repository, and set `targetRevision` /
   `ref.branch` to the branch you promote from.
2. **Provision secrets out-of-band.** Neither tool creates the gateway Secret.
   Create `ubag-gateway-secrets` (the chart's `secrets.existingSecret`) with
   SOPS, sealed-secrets, or external-secrets. The manifests reference it by name.
3. **Bootstrap the controller** (Argo CD or Flux) in the cluster.
4. **Apply the entrypoint manifest:**
   - Argo CD: `kubectl apply -f deploy/gitops/argocd/application.yaml`
   - Flux: `kubectl apply -f deploy/gitops/flux/gitrepository.yaml -f deploy/gitops/flux/helmrelease.yaml`
5. **Promote by commit.** Image tags and value changes are merged to the
   tracked branch; the controller reconciles automatically (`selfHeal`,
   `prune`).

### Multi-region (enterprise)

Register each region's cluster with Argo CD and label it
(`ubag.io/managed=true`, `region=<name>`). The `applicationset.yaml` fans the
same chart out to every matching cluster, stamping a `region` pod label for
observability. Per-region value differences (ingress host, residency settings)
should live in per-region values files referenced from the generator.

## Validate offline

```bash
# YAML well-formedness (no cluster needed)
kubectl apply --dry-run=client -f deploy/gitops/argocd/application.yaml
kubectl apply --dry-run=client -f deploy/gitops/flux/helmrelease.yaml
```

(`kubectl --dry-run=client` only checks client-side structure; full CRD
validation needs the Argo CD / Flux CRDs installed.)

## Requires external infra

- A Kubernetes cluster with Argo CD **or** Flux controllers + their CRDs.
- Git repository access for the controller.
- The externally-managed `ubag-gateway-secrets` Secret.
- (Multi-region) registered regional clusters.
