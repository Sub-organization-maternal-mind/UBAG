# UBAG GitOps Sample Config

Minimal reference layout for deploying UBAG via ArgoCD or Flux.

## Directory structure

```
sample-config/
├── argocd/
│   └── ubag-app.yaml          # ArgoCD Application pointing to deploy/helm/ubag
├── flux/
│   └── ubag-helmrelease.yaml  # Flux HelmRelease pointing to deploy/helm/ubag
├── secrets/
│   └── ubag-secret.yaml.example  # Secret template — DO NOT commit real values
└── README.md
```

## ArgoCD

```sh
kubectl apply -f deploy/gitops/sample-config/argocd/ubag-app.yaml
```

Adjust `repoURL` and `targetRevision` for your fork/tag.

## Flux

Apply the GitRepository source first (see `deploy/gitops/flux/gitrepository.yaml`),
then apply the HelmRelease:

```sh
kubectl apply -f deploy/gitops/flux/gitrepository.yaml
kubectl apply -f deploy/gitops/sample-config/flux/ubag-helmrelease.yaml
```

## Secrets

Copy `secrets/ubag-secret.yaml.example` and fill in real values using your
preferred secret management tool (ExternalSecrets Operator, Sealed Secrets, SOPS,
Vault Agent, etc.). Never commit a file containing real secret values.
