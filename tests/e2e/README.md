# UBAG End-to-End Tests (Live Providers)

These tests drive the **live automation path**: Patchright stealth → real adapter selectors → normalization.

## Running

```sh
# Set required env vars
export UBAG_E2E=1
export UBAG_E2E_GATEWAY=http://localhost:8081
export UBAG_E2E_APP_SECRET=<your_app_secret>
# Optional: provider credentials
export UBAG_E2E_CHATGPT_EMAIL=...
export UBAG_E2E_CLAUDE_EMAIL=...

make e2e
# or: UBAG_E2E=1 npx playwright test tests/e2e/
```

Never run against production without explicit authorization.
Never commit provider credentials to the repository.
CI skips these tests unless UBAG_E2E=1.

## Adapter drift canaries

The drift detection tests (adapter-drift.spec.ts) verify that adapter selectors still exist on the live provider pages. Run regularly against staging.
