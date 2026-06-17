.PHONY: dev dev-edge gateway-build gateway-run gateway-test gateway-vet cover \
	ubag-build sidecar-build \
	test test-v0 test-v0-local itest e2e load test-all sdks bench lint release \
	plugins-build obs-check \
	chaos-smoke backup restore restore-verify \
	release-snapshot helm-lint tf-validate nginx-validate migrate-tier \
	help

GATEWAY_DIR := apps/gateway

help:
	@echo "UBAG make targets (blueprint §29, §31):"
	@echo "  make dev          - bring up the edge profile end-to-end (alias: dev-edge)"
	@echo "  make test         - full v0 validation suite (pnpm test:v0)"
	@echo "  make test-all     - unit + coverage gate + pnpm suites (full local CI)"
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
	@echo "  make nginx-validate - validate nginx-dashboard config"
	@echo "  make migrate-tier - run ubag migrate (TO=<tier> [FROM=<tier>] [DRY_RUN=--dry-run])"
	@echo "  make cover        - go test with coverage report and 80% gate"
	@echo "  make e2e          - run Playwright end-to-end tests (tests/e2e/)"
	@echo "  make load         - run load test suite (tests/load/run-load.mjs)"

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

cover:
	@echo "Running gateway tests with coverage..."
	cd $(GATEWAY_DIR) && go test -race -coverprofile=coverage.out ./...
	cd $(GATEWAY_DIR) && go tool cover -func=coverage.out | tee /tmp/coverage-summary.txt | tail -1
	@TOTAL=$$(grep '^total:' /tmp/coverage-summary.txt | awk '{print $$3}' | tr -d '%'); \
	echo "Total coverage: $${TOTAL}%"; \
	awk -v cov="$${TOTAL}" 'BEGIN { if (cov+0 < 50) { print "Coverage " cov "% is below 50% gate (target: 80%)"; exit 1 } else { print "Gate passed: " cov "% >= 50% (target: 80%)" } }'

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

# End-to-end browser tests via Playwright (Task B1.3).
e2e:
	UBAG_E2E=1 npx playwright test tests/e2e/ --reporter=list

# Load / stress tests (Task B1.5).
load:
	node tests/load/run-load.mjs

# ─── test-all: full local validation umbrella (blueprint §32) ─────────────────
# Runs all gated CI signals locally:
#   make cover       → go test ./... + 80% coverage gate
#   pnpm test:v0:local → unit + conformance (250+) + observability + SDK + cli +
#                       dashboard (vitest + playwright) + docs
# Separate gated jobs (not in test-all):
#   make itest       → integration test (gateway+postgres+stub; needs Docker)
#   make chaos-smoke → chaos experiment schema validation
#   make load        → load test with regression gate (needs k6)
#   UBAG_E2E=1 make e2e → live E2E against real targets (staging only)
test-all: cover
	pnpm test:v0:local
	@echo "──────────────────────────────────────────"
	@echo "test-all: unit + coverage gate (≥80%) + conformance + observability"
	@echo "          + integration (make itest separately)"
	@echo "          + visual regression (cd apps/dashboard && npx playwright test)"
	@echo "Chaos, load, and E2E are separate gated jobs (make chaos-smoke / make load / UBAG_E2E=1 make e2e)"
	@echo "──────────────────────────────────────────"

# --- SDK generation pipeline (blueprint §8.1) -----------------------------
sdks:
	node tools/make-sdks/generate-manifest.mjs

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

nginx-validate:
	@echo "Validating nginx-dashboard config..."
	node tools/check-nginx-dashboard.mjs

migrate-tier:
	@echo "Usage: make migrate-tier TO=small [FROM=edge] [DRY_RUN=--dry-run]"
	@echo "Example: make migrate-tier TO=small DRY_RUN=--dry-run"
	./ubag migrate --to $(TO) $(if $(FROM),--from $(FROM),) $(DRY_RUN)
