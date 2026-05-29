# App icons

Tauri's bundler (`tauri build`, `tauri android build`, `tauri ios build`) reads
the icon files referenced by `bundle.icon` in `../tauri.conf.json`. These are
binary PNG/ICNS/ICO assets and are intentionally **not** committed here.

Generate the full icon set from a single 1024×1024 source image:

```bash
npm run tauri icon ./app-icon.png
```

This produces `32x32.png`, `128x128.png`, `128x128@2x.png`, `icon.icns`,
`icon.ico`, and the Android/iOS mipmap/asset sets. The web frontend build
(`npm run build`) does not require these files.
