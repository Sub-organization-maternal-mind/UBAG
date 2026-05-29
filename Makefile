.PHONY: dev-edge gateway-build gateway-run gateway-test test-v0 test-v0-local

GATEWAY_DIR := apps/gateway

dev-edge: gateway-run

gateway-build:
	cd $(GATEWAY_DIR) && go build ./...

gateway-run:
	cd $(GATEWAY_DIR) && go run ./cmd/gateway

gateway-test:
	cd $(GATEWAY_DIR) && go test ./...

test-v0-local:
	pnpm test:v0:local

test-v0:
	pnpm test:v0
