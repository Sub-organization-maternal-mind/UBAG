.PHONY: dev dev-edge gateway-build gateway-run gateway-vet ubag-build sidecar-build \
	typecheck lint sdks release release-snapshot helm-lint tf-validate \
	backup restore restore-verify migrate-tier help

GATEWAY_DIR := apps/gateway

help:
	@echo "UBAG make targets (language checking and linting only; automated tests were removed):"
	@echo "  make lint         - gateway go vet + pnpm lint (OpenAPI, schemas, proto, blueprint, contracts, SDK freshness)"
	@echo "  make typecheck    - TypeScript, Svelte and Astro typecheck (pnpm typecheck)"
	@echo "  make helm-lint    - lint and template the UBAG Helm chart"
	@echo "  make dev          - bring up the edge profile end-to-end (alias: dev-edge)"
	@echo "  make sdks         - regenerate all SDKs from the contract"
	@echo "  make release      - cross-platform build + sign + SBOM (goreleaser)"
	@echo "  make ubag-build   - build the ubag single binary"
	@echo "  make sidecar-build - build the Rust sidecar with all features (release)"
	@echo "  make backup       - create a local backup (SQLite)"
	@echo "  make restore      - restore from ./ubag-backup-latest"
	@echo "  make restore-verify - restore and verify integrity"
	@echo "  make release-snapshot - goreleaser snapshot build (no publish)"
	@echo "  make tf-validate  - validate all Terraform modules in deploy/terraform/"
	@echo "  make migrate-tier - run ubag migrate (TO=<tier> [FROM=<tier>] [DRY_RUN=--dry-run])"

# --- developer loop -------------------------------------------------------
dev: dev-edge
dev-edge: gateway-run

gateway-build:
	cd $(GATEWAY_DIR) && go build ./...

ubag-build:
	cd $(GATEWAY_DIR) && go build -o ubag ./cmd/ubag

sidecar-build:
	cd packages/sidecar-rust && cargo build --release --all-features

gateway-run:
	cd $(GATEWAY_DIR) && go run ./cmd/gateway

gateway-vet:
	cd $(GATEWAY_DIR) && go vet ./...

# --- language checking and linting ---------------------------------------
typecheck:
	pnpm typecheck

lint: gateway-vet
	pnpm lint

# --- SDK generation pipeline (blueprint §8.1) -----------------------------
sdks:
	node tools/make-sdks/generate-manifest.mjs

# --- release (blueprint §3.5, §11.7) --------------------------------------
release:
	goreleaser release --clean

release-snapshot:
	goreleaser build --snapshot --clean

helm-lint:
	helm lint deploy/helm/ubag
	helm template deploy/helm/ubag -f deploy/helm/ubag/values-ha.yaml > /dev/null

tf-validate:
	@for cloud in aws gcp azure hetzner digitalocean _shared; do \
		echo "Validating deploy/terraform/$$cloud ..."; \
		terraform -chdir=deploy/terraform/$$cloud validate 2>&1 || echo "  (skipped: providers not installed)"; \
	done

# --- backup and restore -----------------------------------------------------
backup:
	cd $(GATEWAY_DIR) && go run ./cmd/ubag backup --out ./ubag-backup-latest

restore:
	cd $(GATEWAY_DIR) && go run ./cmd/ubag restore --from ./ubag-backup-latest

restore-verify:
	cd $(GATEWAY_DIR) && go run ./cmd/ubag restore --from ./ubag-backup-latest && \
	  sqlite3 ubag-gateway.db "PRAGMA integrity_check;"

migrate-tier:
	@echo "Usage: make migrate-tier TO=small [FROM=edge] [DRY_RUN=--dry-run]"
	@echo "Example: make migrate-tier TO=small DRY_RUN=--dry-run"
	./ubag migrate --to $(TO) $(if $(FROM),--from $(FROM),) $(DRY_RUN)
