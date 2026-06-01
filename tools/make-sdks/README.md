# make-sdks

`generate-manifest.mjs` regenerates `generated_contract_manifest.*` in the
TypeScript, Go, and Rust SDKs from `packages/openapi/openapi.yaml` and
`packages/shared-schemas/errors.json`.

Run: `node tools/make-sdks/generate-manifest.mjs`

CI (`tools/check-contracts.mjs`) fails if a committed manifest differs from
the generator output, forcing the SDK to be regenerated when the contract changes.
