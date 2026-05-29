# UBAG Mock Adapter

This is the deterministic v0 mock adapter. It targets Python 3.12 for UBAG
runtime work, while remaining compatible with the local Python 3.9 interpreter.

The adapter accepts a JSON job payload and emits a stable event sequence:

```text
queued -> running -> token... -> completed
```

It does not launch a browser, call a provider, read credentials, or use external
packages. By default it derives a mock text response from the payload prompt.
Tests or callers can force exact stream output with `job.options.mock_tokens`.
