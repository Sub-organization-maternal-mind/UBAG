# Building `prompt-template`

Same toolchain as `response-normalizer`. This guest exports **two** symbols
(`transform` and `hook`), both named in `plugin.manifest.json`.

```bash
esbuild src/plugin.ts --bundle --format=esm --outfile=build/plugin.bundle.js
jco componentize build/plugin.bundle.js \
  --wit ../../wit/ubag-plugin.wit \
  --world-name ubag-plugin \
  --out build/prompt_template.wasm
```

Permissions: only `log` is granted. The plugin performs pure string templating,
so it needs no network, filesystem, or env access — the sandbox denies all
three by default.
