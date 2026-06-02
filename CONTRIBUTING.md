# Contributing to UBAG

Thank you for your interest in contributing! This document explains the contribution process.

## Developer Certificate of Origin (DCO)

All commits must include a `Signed-off-by:` line certifying that you have the right to submit the code:

```bash
git commit -s -m "feat: add awesome feature"
# Produces: Signed-off-by: Your Name <your@email.com>
```

The DCO check in CI will reject any PR with unsigned commits.

## Development workflow

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Make your changes following the code style
4. Run the test suite: `make test-all`
5. Sign your commits: `git commit -s`
6. Open a pull request against `master`

## Commit message format

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(scope): short description
fix(scope): short description
docs: update something
refactor: improve something
test: add tests for something
chore: dependency bump
```

## Code style

- **Go**: `gofmt`; run `go vet ./...` before submitting
- **TypeScript**: ESLint + Prettier (run `pnpm check`)
- **Python**: `ruff check adapters/ apps/worker/`
- **Rust**: `cargo fmt` + `cargo clippy`

## Testing

Run the full test suite before submitting:

```bash
make test-all     # unit + coverage gate + pnpm suites
make itest        # integration tests (requires Docker)
```

## Questions?

Open a [GitHub Discussion](https://github.com/ubag/ubag/discussions) or file an issue.
