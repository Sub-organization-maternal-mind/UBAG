# make-sdks

`generate-manifest.mjs` regenerates `generated_contract_manifest.*` in the
supported TypeScript and Go SDKs from `packages/openapi/openapi.yaml` and
`packages/shared-schemas/errors.json`.

Run: `node tools/make-sdks/generate-manifest.mjs`
Check only: `node tools/make-sdks/generate-manifest.mjs --check`

CI (`tools/check-contracts.mjs`) fails if a committed manifest differs from
the generator output, forcing the SDK to be regenerated when the contract changes.
