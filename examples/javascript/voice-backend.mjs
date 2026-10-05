// Tiny backend for the browser voice example. It holds the UBAG API key
// server-side and hands the browser only what it needs for WebRTC: the SDP
// answer, ICE servers and the short-lived, session-scoped media credential.
//
//   UBAG_TOKEN=... node examples/javascript/voice-backend.mjs   ->  http://127.0.0.1:3000 (PORT overrides)
//
// DEMO ONLY: it binds to 127.0.0.1 and trusts whoever reaches it. In a real
// app put your own user authentication in `authorize()` before exposing it.
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { extname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { clientFromEnv, UbagApiError } from "./sdk.mjs";

const here = fileURLToPath(new URL(".", import.meta.url));
const sdkDist = resolve(here, "../../packages/sdk-typescript/dist");
const client = clientFromEnv();
const port = Number(process.env.PORT ?? 3000);
const owned = new Set(); // session ids this server created; browsers may only touch these
const heartbeats = new Map();

function authorize(_req) {
  return true; // replace with your session/cookie/JWT check
}

function startHeartbeat(id) {
  // Keeps the provider lease alive while the call runs; a vanished browser stops
  // being renewed once terminate is called, or the lease lapses on its own.
  heartbeats.set(id, setInterval(() => client.renewVoiceSessionLease(id).catch(() => stopHeartbeat(id)), 60_000));
}

function stopHeartbeat(id) {
  clearInterval(heartbeats.get(id));
  heartbeats.delete(id);
}

const readJson = async (req) => {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 256 * 1024) throw Object.assign(new Error("body too large"), { status: 413 });
    chunks.push(chunk);
  }
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : {};
};

const send = (res, status, body, type = "application/json", headers = {}) => {
  res.writeHead(status, { "content-type": type, "cache-control": "no-store", ...headers });
  res.end(typeof body === "string" || Buffer.isBuffer(body) ? body : JSON.stringify(body));
};

async function handleApi(req, res, url) {
  if (!authorize(req)) return send(res, 401, { error: "unauthorized" });
  const parts = url.pathname.split("/").filter(Boolean); // api voice sessions [id [action]]
  if (req.method === "POST" && parts.length === 3) {
    const { target } = await readJson(req);
    const created = await client.createVoiceSession({ target: String(target ?? "chatgpt_web") });
    owned.add(created.session_id);
    startHeartbeat(created.session_id);
    return send(res, 200, { session_id: created.session_id, status: created.status });
  }
  const id = parts[3];
  if (req.method !== "POST" || parts.length !== 5 || !owned.has(id)) return send(res, 404, { error: "not found" });
  if (parts[4] === "connect") {
    const { sdp_offer } = await readJson(req);
    return send(res, 200, await client.connectVoiceSession(id, String(sdp_offer ?? "")));
  }
  if (parts[4] === "terminate") {
    stopHeartbeat(id);
    owned.delete(id);
    return send(res, 200, await client.terminateVoiceSession(id));
  }
  return send(res, 404, { error: "not found" });
}

const types = { ".js": "text/javascript", ".html": "text/html; charset=utf-8", ".map": "application/json" };

createServer(async (req, res) => {
  const url = new URL(req.url, "http://localhost");
  try {
    if (url.pathname.startsWith("/api/voice/sessions")) return await handleApi(req, res, url);
    if (url.pathname === "/") return send(res, 200, readFileSync(join(here, "voice-browser.html")), types[".html"]);
    if (url.pathname.startsWith("/sdk/")) {
      // The browser page imports the built SDK; bundle it in a real app.
      const file = join(sdkDist, normalize(url.pathname.slice(5)).replace(/^(\.\.[/\\])+/, ""));
      if (!file.startsWith(sdkDist)) return send(res, 403, "forbidden", "text/plain");
      return send(res, 200, readFileSync(file), types[extname(file)] ?? "text/plain");
    }
    send(res, 404, "not found", "text/plain");
  } catch (error) {
    if (error instanceof UbagApiError) {
      // Pass the gateway's overload guidance through so the page can show it.
      const headers = error.headers["retry-after"] ? { "retry-after": error.headers["retry-after"] } : {};
      return send(res, error.status, { error: error.error ?? error.body, retry_after_ms: error.retryAfterMs }, "application/json", headers);
    }
    send(res, error.status ?? 500, { error: error.message });
  }
}).listen(port, "127.0.0.1", () => console.log(`Open http://127.0.0.1:${port} and allow the microphone.`));

process.on("SIGINT", async () => {
  for (const id of owned) await client.terminateVoiceSession(id).catch(() => undefined);
  process.exit(0);
});
