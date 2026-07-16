# Live-browser bridge (local operator tool)

Streams a **real Chrome** into the dashboard's **Browser Sessions** page and
forwards your mouse/keyboard back to it, so an operator can log into providers
(ChatGPT, Claude, Gemini, DeepSeek, …) interactively from inside the dashboard —
no separate window, no Docker, no VNC.

It's the ToS-safe "human logs in once, in their own session" model: the bridge
never automates a login; it just gives you a live, interactive view of a normal
Chrome. The Chrome uses a **persistent profile** (`chrome-profile/`, gitignored)
so your logins are remembered across restarts.

## How it works

- Launches Chrome with `--remote-debugging-port` + a persistent `--user-data-dir`,
  and occlusion/backgrounding disabled (so it keeps painting while it sits in the
  background and you drive it from the dashboard).
- Connects over the Chrome DevTools Protocol, runs `Page.startScreencast`, and
  relays JPEG frames to the dashboard over a WebSocket.
- Receives mouse/keyboard/navigation events from the dashboard and dispatches
  them via CDP `Input.*` / `Page.navigate`.

Zero external dependencies (Node 22+ built-in `WebSocket` client + a hand-rolled
RFC6455 server). Loopback only.

## Run it

The desktop launcher (`tools/local-launcher/start-ubag.bat`) starts this
automatically. To run it on its own:

```bash
node tools/live-browser/bridge.mjs
```

Then open the dashboard's **Browser Sessions** page — the **Live Browser** panel
below the KPI cards connects automatically.

## Configuration (env vars, all optional)

| Var | Default | Purpose |
|---|---|---|
| `UBAG_LIVE_BROWSER_PORT` | `58090` | Dashboard-facing WebSocket port |
| `UBAG_LIVE_BROWSER_CDP_PORT` | `58091` | Chrome remote-debugging port |
| `UBAG_LIVE_BROWSER_PROFILE` | `./chrome-profile` | Persistent Chrome profile dir |
| `UBAG_LIVE_BROWSER_START_URL` | `https://chatgpt.com` | Initial page |
| `UBAG_CHROME_PATH` | auto-detected | Chrome/Edge executable |
| `UBAG_LIVE_BROWSER_QUALITY` | `60` | JPEG quality (1–100) |
| `UBAG_LIVE_BROWSER_MAX_WIDTH` | `1280` | Max frame width |

Local development only; not part of the build/test pipeline.
