# Building `response-normalizer`

This example ships guest **source** plus a manifest. The mock host loads
`src/plugin.ts` directly. To produce the real artifact referenced by the
manifest (`build/response_normalizer.wasm`), compile to a WASI component.

## TypeScript / JavaScript guests

Use [ComponentizeJS](https://github.com/bytecodealliance/ComponentizeJS) +
[`jco`](https://github.com/bytecodealliance/jco):

```bash
# 1. bundle the guest to a single ESM file
esbuild src/plugin.ts --bundle --format=esm --outfile=build/plugin.bundle.js

# 2. componentize against the UBAG plugin WIT world
jco componentize build/plugin.bundle.js \
  --wit ../../wit/ubag-plugin.wit \
  --world-name ubag-plugin \
  --out build/response_normalizer.wasm
```

## Rust guests (alternative)

```bash
cargo component build --release
cp target/wasm32-wasip2/release/response_normalizer.wasm build/
```

The exported names must match `entrypoint.exports` in `plugin.manifest.json`
(`transform`). The host only grants `log` and `clock`; any attempt to call
`fetch`, `read_file`, or `get_env` is rejected by the sandbox.
