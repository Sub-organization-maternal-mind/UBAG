.PHONY: dev dev-edge gateway-build gateway-run gateway-test gateway-vet \
	ubag-build sidecar-build \
	test test-v0 test-v0-local itest sdks bench lint release \
	plugins-build obs-check \
	chaos-smoke backup restore restore-verify \
	release-snapshot helm-lint tf-validate caddy-validate migrate-tier \
	help

GATEWAY_DIR := apps/gateway

help:
	@echo "UBAG make targets (blueprint §29, §31):"
	@echo "  make dev          - bring up the edge profile end-to-end (alias: dev-edge)"
	@echo "  make test         - full v0 validation suite (pnpm test:v0)"
	@echo "  make itest        - integration tests (gateway+worker+DB+mock target)"
	@echo "  make sdks         - regenerate all SDKs from the contract"
	@echo "  make bench        - run the benchmark suite"
	@echo "  make lint         - lint Go + contracts + schemas + proto"
	@echo "  make release      - cross-platform build + sign + SBOM (goreleaser)"
	@echo "  make ubag-build   - build the ubag single binary"
	@echo "  make sidecar-build - build the Rust sidecar with all features (release)"
	@echo "  make chaos-smoke  - validate chaos experiment schemas and steady-state evaluator"
	@echo "  make backup       - create a local backup (SQLite)"
	@echo "  make restore      - restore from ./ubag-backup-latest"
	@echo "  make restore-verify - restore and verify integrity"
	@echo "  make release-snapshot - goreleaser snapshot build (no publish)"
	@echo "  make helm-lint    - lint and template the UBAG Helm chart"
	@echo "  make tf-validate  - validate all Terraform modules in deploy/terraform/"
	@echo "  make caddy-validate - validate Caddy config against custom module list"
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

gateway-test:
	cd $(GATEWAY_DIR) && go test ./...

gateway-vet:
	cd $(GATEWAY_DIR) && go vet ./...

# --- test surface (blueprint §32) -----------------------------------------
test: test-v0
test-v0-local:
	pnpm test:v0:local
test-v0:
	pnpm test:v0

# Integration tests: gateway + worker + DB + mock target in containers (§32.2).
# Stable entrypoint; the runner is added in the Phase 0/10 testing track.
itest:
	node tools/run-integration-tests.mjs

# --- SDK generation pipeline (blueprint §8.1) -----------------------------
sdks:
	node tools/make-sdks/generate.mjs

# --- benchmarks (blueprint §19.5, §36.3) ----------------------------------
bench:
	node tools/benchmark/run.mjs

# --- lint -----------------------------------------------------------------
lint: gateway-vet
	pnpm test:schema

# --- release (blueprint §3.5, §11.7) --------------------------------------
release:
	goreleaser release --clean

# --- Phase 6: WASM plugins + observability --------------------------------

# plugins-build: compile the WAT test fixture to .wasm (requires wat2wasm).
# The committed .wasm files are pre-compiled so CI does not need wat2wasm.
plugins-build:
	wat2wasm apps/gateway/internal/plugins/testdata/echo_transform.wat \
	  -o apps/gateway/internal/plugins/testdata/echo_transform.wasm
	@echo "echo_transform.wasm rebuilt"

# obs-check: validate metrics cardinality budget and Grafana dashboards.
obs-check:
	node tools/check-metrics-cardinality.mjs deploy/grafana/dashboards/09-slo-overview.json || true
	node tools/check-grafana-dashboards.mjs deploy/grafana/dashboards/

# --- Phase 7: Reliability, chaos, backup ------------------------------------

chaos-smoke:
	python -m pytest tests/chaos/tests/ -v

backup:
	cd $(GATEWAY_DIR) && go run ./cmd/ubag backup --out ./ubag-backup-latest

restore:
	cd $(GATEWAY_DIR) && go run ./cmd/ubag restore --from ./ubag-backup-latest

restore-verify:
	cd $(GATEWAY_DIR) && go run ./cmd/ubag restore --from ./ubag-backup-latest && \
	  sqlite3 ubag-gateway.db "PRAGMA integrity_check;"

# --- Phase 8 release and validation targets ---

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

caddy-validate:
	@echo "Note: requires xcaddy build with caddy-ratelimit + coraza-caddy modules"
	node tools/check-caddy.mjs

migrate-tier:
	@echo "Usage: make migrate-tier TO=small [FROM=edge] [DRY_RUN=--dry-run]"
	@echo "Example: make migrate-tier TO=small DRY_RUN=--dry-run"
	./ubag migrate --to $(TO) $(if $(FROM),--from $(FROM),) $(DRY_RUN)
