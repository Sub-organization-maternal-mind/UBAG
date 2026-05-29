# UBAG Go SDK

Go client for the UBAG v0 REST gateway, stable errors, idempotency, and shared conformance fixtures.

The package is dependency-free and mirrors the current TypeScript and Python SDK baseline:

- Health, readiness, and version helpers.
- Job create, get, list, cancel, and retry helpers.
- Caller-supplied or generated idempotency keys.
- Stable `APIError` envelopes for gateway errors.
- `http.Client` injection for local conformance and mock-gateway tests.

## Local Tests

Run through the root script, which uses `go` from `PATH` or the local Codex
portable toolchain:

```powershell
cmd /c pnpm test:sdk:go
```

Or run directly from this package directory when Go is available:

```powershell
cd packages/sdk-go
go test ./...
```

The tests load the shared scenarios from
`packages/conformance/fixtures/v0/scenarios.json`.

## Example

```go
package main

import (
	"context"
	"fmt"

	ubag "github.com/ubag/ubag-go"
)

func main() {
	client, err := ubag.NewClient("http://127.0.0.1:7878", ubag.WithAppSecret("dev-secret"))
	if err != nil {
		panic(err)
	}

	job, err := client.CreateJob(context.Background(), ubag.JSON{
		"client": ubag.JSON{"app_id": "demo", "app_version": "0.0.0"},
		"job": ubag.JSON{
			"target":       "mock_target",
			"command_type": "echo",
			"input":        ubag.JSON{"prompt": "Hello UBAG"},
		},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(job["job_id"])
}
```
