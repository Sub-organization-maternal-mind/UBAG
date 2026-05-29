# UBAG Monitor — Mobile Monitoring App

Read-only operations monitoring for a [UBAG](../../README.md) gateway, built as
a **Tauri 2 mobile** app (Rust + Svelte). One Svelte codebase ships to iOS,
Android, and a desktop window for development. The native binary stays small
(target < 5 MB) per the blueprint (`UBAG_World_Class_Blueprint_v2.md` §3.1).

It is a **monitor + basic actions** surface — no heavy admin. It talks to the
gateway REST API over HTTP and shows:

| Screen | What it does |
| --- | --- |
| **Settings** | Configure the gateway URL and app secret, test connectivity. The secret is stored in the device's secure credential store and never logged. |
| **Overview** | Gateway `health` / `ready` / `version`, readiness dependencies, and key Prometheus metrics. |
| **Jobs** | Cursor-backed job list with a status filter. Tap a job to open it. |
| **Job detail** | Job metadata plus a **live event timeline** (polls `GET /v1/jobs/{id}/events?after_sequence=` while the job is non-terminal; the gateway also exposes `GET /v1/sse/jobs/{id}` for SSE). |
| **Alerts & Audit** | Audit feed (`/v1/audit`) and configured webhook destinations (`/v1/webhooks`). |

Styling is the **NAJM / Hallmark** visual system ported from
[`design.md`](../../design.md) and
[`apps/dashboard/src/styles/tokens.css`](../dashboard/src/styles/tokens.css):
warm cream paper, terracotta accent, geometric display headings, mono
operational labels — mobile-first, large (≥48 px) tap targets, safe-area aware,
and `prefers-reduced-motion` friendly.

## Gateway API contract

- **Base URL**: configurable in Settings, default `http://127.0.0.1:8080`.
- **Headers**: `Authorization: Bearer <app-secret>` and
  `UBAG-Api-Version: 2026-05-22` on every authenticated request.
- **Endpoints used** (all `GET`, read-only): `/v1/health`, `/v1/ready`,
  `/v1/version`, `/v1/metrics`, `/v1/jobs`, `/v1/jobs/{id}`,
  `/v1/jobs/{id}/events`, `/v1/sse/jobs/{id}`, `/v1/webhooks`, `/v1/audit`,
  `/v1/cache`.

Shapes mirror [`packages/openapi/openapi.yaml`](../../packages/openapi/openapi.yaml).

### Why HTTP goes through the Rust plugin

On a device the webview origin is `tauri://localhost`. A self-hosted gateway
will not emit CORS headers for that origin, so browser `fetch` would be blocked.
This app routes all REST calls through `@tauri-apps/plugin-http`, which issues
requests from Rust and bypasses CORS. During plain web development it
transparently falls back to the platform `fetch`. See
[`src/lib/api.ts`](src/lib/api.ts).

### Secret storage

The gateway app-secret is a bearer credential. It is stored via native Rust
commands (`secure_store_set/get/delete` in
[`src-tauri/src/lib.rs`](src-tauri/src/lib.rs)) backed by the OS credential
store (`keyring`): Windows Credential Manager, macOS / iOS Keychain, Linux
Secret Service. It is **never** written to disk in plaintext, placed in a URL,
or logged. In web-only dev mode (no native layer) it is held in memory and
cleared on reload, with a one-time console warning. Non-secret prefs (URL,
refresh interval) live in `localStorage`.

> **Stronghold alternative.** If you prefer an app-managed encrypted vault over
> the OS keychain, swap the `keyring` calls for
> [`tauri-plugin-stronghold`](https://v2.tauri.app/plugin/stronghold/). It needs
> a password to unlock the vault on launch; document that UX before switching.

## Web frontend build / check (works offline)

After `npm install`, these run fully offline (no gateway, no mobile SDKs):

```bash
npm run build      # vite build → dist/ (the static bundle Tauri ships)
npm run check      # svelte-check type/template diagnostics
npm run dev        # vite dev server on http://localhost:1420 (browser preview)
```

`npm run dev` opens in a normal browser; native secure storage and the HTTP
plugin fall back to web equivalents so you can iterate on UI offline.

## Running the native app

```bash
npm run tauri:dev          # desktop dev window (fast inner loop)
npm run tauri:android:dev  # Android emulator/device
npm run tauri:ios:dev      # iOS simulator/device (macOS only)
```

First-time mobile setup generates the native projects under `src-tauri/gen/`:

```bash
npm run tauri:android:init
npm run tauri:ios:init     # macOS only
npm run tauri icon ./app-icon.png   # generate bundle icons (see src-tauri/icons/README.md)
```

Production builds:

```bash
npm run tauri:android:build   # → .apk / .aab
npm run tauri:ios:build       # → .ipa (macOS only)
```

## External toolchains / SDKs required (external-activation items)

These are **not installable from this repo** and are not attempted by the build.
They must be activated on the build machine before native builds work:

### Shared (all native targets)
- **Rust** ≥ 1.78 (`rustup`) and Cargo.
- **Tauri CLI v2** — provided as a dev dependency (`@tauri-apps/cli`); invoked
  via `npm run tauri …`.
- System webview/build deps per the
  [Tauri v2 prerequisites](https://v2.tauri.app/start/prerequisites/)
  (e.g. WebView2 on Windows; `webkit2gtk`, `libsoup` on Linux).

### Android
- **Android Studio** + **Android SDK** (Platform 33+ recommended).
- **Android NDK** (set `NDK_HOME`).
- **JDK 17** (set `JAVA_HOME`).
- Rust targets: `aarch64-linux-android`, `armv7-linux-androideabi`,
  `i686-linux-android`, `x86_64-linux-android`
  (`rustup target add …`).
- Env: `ANDROID_HOME` / `ANDROID_SDK_ROOT`.

### iOS (macOS only)
- **Xcode** + Command Line Tools, a configured signing team.
- **CocoaPods** (`brew install cocoapods`).
- Rust targets: `aarch64-apple-ios`, `aarch64-apple-ios-sim`,
  `x86_64-apple-ios` (`rustup target add …`).

### Push notifications (TODO)
Alert push is stubbed (`register_push_token` in `src-tauri/src/lib.rs`).
Wiring it requires external services: **Firebase Cloud Messaging** (Android) and
**APNs** (iOS), plus device-token registration against the gateway. See the
`TODO(push)` block in the Rust source.

## Project layout

```
apps/mobile/
├── index.html                 # Vite entry
├── package.json               # @ubag/mobile (own deps)
├── vite.config.ts             # Tauri-aware Vite config (port 1420)
├── svelte.config.js
├── tsconfig.json
├── public/favicon.svg
├── src/
│   ├── main.ts                # mounts the Svelte app
│   ├── App.svelte             # shell: header + bottom tabs + screen stack
│   ├── app.css                # NAJM/Hallmark tokens (mobile-first)
│   ├── lib/
│   │   ├── types.ts           # API types (mirrors openapi.yaml)
│   │   ├── api.ts             # gateway REST client (+ Prometheus parser)
│   │   ├── settings.ts        # non-secret prefs store
│   │   ├── secureStore.ts     # native secret bridge + web fallback
│   │   └── format.ts          # status tones, time/number formatting
│   └── components/
│       ├── OverviewView.svelte
│       ├── JobsView.svelte
│       ├── JobDetailView.svelte   # live polling timeline
│       ├── AlertsView.svelte
│       ├── SettingsView.svelte
│       ├── StatusBadge.svelte
│       └── AsyncState.svelte
└── src-tauri/
    ├── Cargo.toml
    ├── build.rs
    ├── tauri.conf.json
    ├── capabilities/default.json  # core IPC + HTTP scope
    ├── icons/README.md            # how to generate icons
    └── src/
        ├── main.rs
        └── lib.rs                 # secure storage + push stub commands
```
