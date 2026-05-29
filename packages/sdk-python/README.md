# UBAG Python SDK

Python client for the UBAG v0 REST gateway, stable errors, idempotency, and shared conformance fixtures.

The package is intentionally dependency-free and mirrors the current TypeScript
and Go SDK surface:

- Health, readiness, and version helpers.
- Job create, get, list, cancel, and retry helpers.
- Caller-supplied or generated idempotency keys.
- Stable `UbagApiError` envelopes for gateway errors.
- Injectable transport for fixture and mock-gateway tests.

## Local Tests

Run from the repository root:

```powershell
python -m unittest discover packages/sdk-python/tests
```

The tests load the shared scenarios from
`packages/conformance/fixtures/v0/scenarios.json` and assert the Python client
preserves request paths, required headers, expected bodies, success payloads,
and error envelopes.

## Example

```python
from ubag import UbagClient

client = UbagClient("http://127.0.0.1:7878", app_secret="dev-secret")

job = client.create_job({
    "client": {"app_id": "demo", "app_version": "0.0.0"},
    "job": {
        "target": "mock_target",
        "command_type": "echo",
        "input": {"prompt": "Hello UBAG"},
    },
})

print(job["job_id"])
```
