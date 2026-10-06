# Synthetic chat provider (P7.1)

A loopback-only fake "web chat" so load and acceptance scenarios drive a **real browser** through the worker's live
engine and warm daemon. The `mock` target never reaches the daemon, so it cannot show pool or streaming gains. Zero
dependencies; nothing here is part of the default adapter registry.

Safe-mode: binds `127.0.0.1`/`localhost`/`::1` only and rejects non-loopback `Host` headers. No login, password,
credential or CAPTCHA behaviour; the signed-out page is a bare `Sign in` anchor that goes nowhere.

## Run it

```bash
node tools/synthetic-provider/server.mjs --port 4799      # prints {"listening":"http://127.0.0.1:4799/"}
```

Worker and gateway (both default OFF; the gateway forwards both variables to the worker):

| Variable | Meaning |
|---|---|
| `UBAG_SYNTHETIC_PROVIDER=1` | Registers the `synthetic_chat` live target in the worker, and routes it to the warm daemon in the gateway. |
| `UBAG_SYNTHETIC_PROVIDER_URL` | Fixture origin, default `http://127.0.0.1:4799/`. Must be `http` on loopback; anything else leaves the target unregistered. |

The browser must be able to reach that loopback address, so run the fixture where the browser runs. Submit jobs with
`target: "synthetic_chat"`, `options.headless: true` for a local headless run.

## Scenarios

Pick one per prompt with a leading directive: `[[synthetic scenario=slow_think think_ms=2500]] explain X`. Explicit keys
override the preset; unknown names, bad values or out-of-range values give HTTP 400 (the page shows an error banner and
no assistant turn), never a silent default run.

| Preset | What the page does | Parameters it sets |
|---|---|---|
| `default` | Echoes `Synthetic answer: <prompt>`. | none |
| `slow_think` | Assistant turn exists, empty, Stop control visible, for `think_ms` before the first byte. | `think_ms=3000` |
| `stream_cadence` | Fixed-size chunks every `stream_ms`. | `chars=600 chunk_chars=24 stream_ms=120` |
| `long_output` | Deterministic filler text. | `chars=200000 chunk_chars=4096` |
| `truncated` | Stops after `truncate_after` chars and stalls with the Stop control still up (a hang), then drops the connection after `stall_ms`. A deadline-cut stream. | `chars=4000 truncate_after=600` |
| `dropped` | Kills the connection after `drop_after` chars. The turn goes `data-state="error"` and an error banner shows. | `chars=4000 drop_after=600` |

Other keys: `think_ms stream_ms chunk_chars chars truncate_after drop_after stall_ms` (non-negative integers, capped; see
`LIMITS` in `server.mjs`; `chars` max 2 MiB).

- **Signed out**: `GET /?signed_out=1`, `--signed-out`, or `POST /__control {"signed_out":true}`. The worker must fail
  closed with `manual_login_required`.
- **Slow attachment accept**: `--attach-ms 1500` or `POST /__control {"attach_ms":1500}`. The hidden file input uploads
  on selection; Send stays disabled until every upload is accepted, so the delay lands in `provider_submit`. Above about
  25 s the driver's 30 s click timeout fires, which is itself a realistic failure.
- **Counters**: `GET /__stats` (`page_loads`, `chat_requests`, `chat_completed`, `chat_truncated`, `chat_dropped`,
  `chat_rejected`, `uploads`, `upload_bytes`, `active_streams`, `max_active_streams`); `POST /__stats/reset`.

## What the worker sees

The selectors live in `apps/worker/ubag_worker/live/synthetic.py` and key off `data-synthetic="..."` markers, so the page
styling can change freely. `tests` check them statically against the served HTML (`apps/worker/tests/test_synthetic_provider.py`).

Known limits, stated so nobody over-reads a run:

- A dropped connection is only visible as an error banner and partial text. The engine has no provider-error signal, so
  by reading `stream_response` it ends that turn as `completed` with the partial text (`UBAG_WORKER_STRICT_STREAM_END`
  covers deadline cuts only). No test asserts that engine verdict.
- Numbers measured against this fixture on a laptop are NON-AUTHORITATIVE and say nothing about real providers.
