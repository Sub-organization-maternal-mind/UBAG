.PHONY: dev dev-edge gateway-build gateway-run gateway-test gateway-vet \
	ubag-build sidecar-build \
	test test-v0 test-v0-local itest sdks bench lint release \
	plugins-build obs-check \
	chaos-smoke backup restore restore-verify \
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
