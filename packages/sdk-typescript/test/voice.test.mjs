import { test } from "node:test";
import assert from "node:assert/strict";
import { UbagApiError, UbagClient, VoiceMediaClient, audioPart, imagePart, textPart } from "../dist/index.js";

const json = (body, init = {}) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" }, ...init });

function makeClient(handler) {
  const calls = [];
  const client = new UbagClient({
    baseUrl: "https://gateway.example",
    appSecret: "s3cret",
    sidecarDiscovery: false,
    fetch: async (url, init) => {
      const call = {
        url: new URL(String(url)),
        method: init.method,
        auth: init.headers.get("Authorization"),
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      };
      calls.push(call);
      return handler(call);
    },
  });
  return { client, calls };
}

test("voice session methods hit the documented routes with typed bodies", async () => {
  const { client, calls } = makeClient((call) => {
    const path = call.url.pathname;
    if (path === "/v1/voice/sessions" && call.method === "POST") {
      return json({ kind: "voice_session", session_id: "voice_1", status: "queued", session: { session_id: "voice_1", status: "queued" } }, { status: 202 });
    }
    if (path.endsWith("/connect")) {
      return json({
        kind: "voice_session_connection", session_id: "voice_1", status: "connecting", sdp_answer: "v=0",
        media_credential: "cred", media_credential_expires_ms: 1,
        ice_servers: [{ urls: ["turn:t:3478"], username: "u", credential: "c" }],
      });
    }
    if (path.endsWith("/mute")) return json({ kind: "voice_session_control", session_id: "voice_1", muted: true });
    if (path.endsWith("/renew")) return json({ kind: "voice_session_control", session_id: "voice_1", lease_expires_at: "2026-10-05T00:10:00Z" });
    if (path.endsWith("/terminate")) return json({ kind: "voice_session", session: { session_id: "voice_1", status: "terminated" } });
    if (path === "/v1/voice/sessions") return json({ api_version: "2026-05-22", kind: "voice_sessions", data: [{ session_id: "voice_1" }], next_cursor: null });
    return json({ kind: "voice_session", session: { session_id: "voice_1", status: "connected" } });
  });

  const created = await client.createVoiceSession({ target: "chatgpt_web", mode: "live", ttl_seconds: 60 });
  assert.equal(created.status, "queued");
  assert.deepEqual(calls[0].body, { target: "chatgpt_web", mode: "live", ttl_seconds: 60 });
  assert.equal(calls[0].auth, "Bearer s3cret");

  const connect = await client.connectVoiceSession("voice_1", "offer-sdp");
  assert.equal(connect.ice_servers[0].username, "u");
  assert.deepEqual(calls[1].body, { sdp_offer: "offer-sdp" });
  assert.equal(calls[1].method, "POST");

  const muted = await client.muteVoiceSession("voice_1", true);
  assert.equal(muted.muted, true);
  assert.deepEqual(calls[2].body, { muted: true });

  const renewed = await client.renewVoiceSessionLease("voice_1");
  assert.equal(renewed.lease_expires_at, "2026-10-05T00:10:00Z");
  assert.equal(calls[3].method, "POST");
  assert.equal(calls[3].body, undefined);

  const got = await client.getVoiceSession("voice_1");
  assert.equal(got.session.status, "connected");
  assert.equal(calls[4].method, "GET");

  const list = await client.listVoiceSessions({ limit: 5, target: "chatgpt_web" });
  assert.equal(list.data.length, 1);
  assert.equal(calls[5].url.search, "?limit=5&target=chatgpt_web");

  const ended = await client.terminateVoiceSession("voice_1");
  assert.equal(ended.session.status, "terminated");
  assert.equal(calls[6].url.pathname, "/v1/voice/sessions/voice_1/terminate");
  assert.equal(calls[6].method, "POST");
});

test("session ids are path-encoded", async () => {
  const { client, calls } = makeClient(() => json({ kind: "voice_session", session: {} }));
  await client.getVoiceSession("a/b c");
  assert.equal(calls[0].url.pathname, "/v1/voice/sessions/a%2Fb%20c");
});

test("429 queue-full surfaces Retry-After on UbagApiError", async () => {
  const { client } = makeClient(() =>
    json(
      { error: { code: "UBAG-VOICE-QUEUE-FULL-004", category: "voice", message: "queue full", retryable: true, trace_id: "t" } },
      { status: 429, headers: { "content-type": "application/json", "retry-after": "7" } },
    ),
  );
  await assert.rejects(
    () => client.createVoiceSession({ target: "chatgpt_web" }),
    (error) => {
      assert.ok(error instanceof UbagApiError);
      assert.equal(error.status, 429);
      assert.equal(error.code, "UBAG-VOICE-QUEUE-FULL-004");
      assert.equal(error.retryable, true);
      assert.equal(error.retryAfterMs, 7000);
      return true;
    },
  );
});

test("listCapabilities returns typed per-target voice flags", async () => {
  const { client, calls } = makeClient(() =>
    json({
      api_version: "2026-05-22", kind: "capabilities",
      data: [{ target: "chatgpt_web", voice: { supported: true, configured: true, verified: false, verified_note: "pending", available: true, free_resources: 1, live: true, utterance_jobs: true, available_accounts: 1 }, inline_message_parts: { image_url: ["image/png"], input_audio: ["audio/wav"], remote_urls: false } }],
    }),
  );
  const capabilities = await client.listCapabilities();
  assert.equal(calls[0].url.pathname, "/v1/capabilities");
  const voice = capabilities.data[0].voice;
  assert.equal(voice.verified, false);
  assert.equal(voice.available, true);
  assert.equal(voice.free_resources, 1);
});

test("createChatCompletion carries multimodal parts built by the helpers", async () => {
  const { client, calls } = makeClient(() =>
    json({ id: "c1", object: "chat.completion", model: "chatgpt_web", choices: [{ index: 0, message: { role: "assistant", content: "a cat" }, finish_reason: "stop" }] }),
  );
  const reply = await client.createChatCompletion({
    model: "chatgpt_web",
    messages: [{ role: "user", content: [textPart("what is this?"), imagePart(new Uint8Array([1, 2, 3]), "image/png"), audioPart(new Uint8Array([4, 5]), "wav")] }],
  });
  assert.equal(reply.choices[0].message.content, "a cat");
  assert.equal(calls[0].url.pathname, "/v1/openai/chat/completions");
  assert.deepEqual(calls[0].body.messages[0].content, [
    { type: "text", text: "what is this?" },
    { type: "image_url", image_url: { url: "data:image/png;base64,AQID" } },
    { type: "input_audio", input_audio: { data: "BAU=", format: "wav" } },
  ]);
});

test("part builders validate their inputs", () => {
  assert.deepEqual(imagePart("data:image/png;base64,AAAA"), { type: "image_url", image_url: { url: "data:image/png;base64,AAAA" } });
  assert.throws(() => imagePart("https://example.com/cat.png"), TypeError);
  assert.throws(() => imagePart(new Uint8Array([1])), TypeError);
  assert.throws(() => audioPart(new Uint8Array([1]), "ogg"), TypeError);
  assert.equal(imagePart(new Uint8Array([1, 2, 3]).buffer, "image/jpeg").image_url.url, "data:image/jpeg;base64,AQID");
});

// ── VoiceMediaClient with a fake RTCPeerConnection ────────────────────────

class FakeChannel extends EventTarget {
  readyState = "connecting";
  sent = [];
  send(data) { this.sent.push(JSON.parse(data)); }
  close() { this.readyState = "closed"; }
  open() { this.readyState = "open"; this.dispatchEvent(new Event("open")); }
  emit(payload) {
    const event = new Event("message");
    event.data = JSON.stringify(payload);
    this.dispatchEvent(event);
  }
}

class FakePeerConnection extends EventTarget {
  constructor(config) {
    super();
    this.config = config;
    this.iceGatheringState = "new";
    this.localDescription = null;
    this.tracks = [];
    this.closed = false;
    this.order = [];
  }
  addTrack(track) { this.tracks.push(track); }
  createDataChannel(label) { this.order.push(`channel:${label}`); this.channel = new FakeChannel(); this.channel.label = label; return this.channel; }
  async createOffer() { this.order.push("offer"); return { type: "offer", sdp: "offer-sdp" }; }
  async setLocalDescription(description) {
    this.order.push("local");
    this.localDescription = description;
    queueMicrotask(() => { this.iceGatheringState = "complete"; this.dispatchEvent(new Event("icegatheringstatechange")); });
  }
  async setRemoteDescription(description) { this.order.push("remote"); this.remote = description; }
  getConfiguration() { return { bundlePolicy: "balanced", iceServers: this.config.iceServers }; }
  setConfiguration(configuration) { this.order.push("configure"); this.config = configuration; }
  close() { this.closed = true; }
}

const connectResponse = {
  kind: "voice_session_connection", session_id: "voice_1", status: "connecting", sdp_answer: "answer-sdp",
  media_credential: "cred-123", media_credential_expires_ms: 1,
  ice_servers: [{ urls: ["turn:turn.example:3478"], username: "u", credential: "c" }],
};

test("VoiceMediaClient: offer/answer, ICE servers, control auth, mute, close", async () => {
  let pc;
  const track = { enabled: true };
  const offers = [];
  const events = [];
  const media = new VoiceMediaClient({
    stream: { getTracks: () => [track] },
    iceServers: [{ urls: ["stun:stun.example:3478"] }],
    createPeerConnection: (config) => (pc = new FakePeerConnection(config)),
    exchange: async (sdp) => { offers.push(sdp); return connectResponse; },
    onEvent: (event) => events.push(event),
  });

  const response = await media.connect();
  assert.equal(response.session_id, "voice_1");
  assert.deepEqual(offers, ["offer-sdp"]);
  assert.deepEqual(pc.tracks, [track]);
  // The control channel is created before the offer so it is negotiated in it.
  assert.deepEqual(pc.order, ["channel:control", "offer", "local", "configure", "remote"]);
  assert.deepEqual(pc.config.iceServers, connectResponse.ice_servers);
  assert.equal(pc.config.bundlePolicy, "balanced");
  assert.deepEqual(pc.remote, { type: "answer", sdp: "answer-sdp" });

  // Commands are refused until the gateway acknowledges the credential.
  pc.channel.open();
  assert.deepEqual(pc.channel.sent, [{ op: "auth", credential: "cred-123" }]);
  assert.throws(() => media.mute(true), /not authenticated/);

  pc.channel.emit({ event: "auth", ok: true });
  await media.ready;
  assert.equal(media.isAuthenticated, true);

  media.mute(true);
  assert.deepEqual(pc.channel.sent.at(-1), { op: "mute", muted: true });
  assert.equal(track.enabled, false);
  media.mute(false);
  assert.equal(track.enabled, true);

  media.ping();
  assert.deepEqual(pc.channel.sent.at(-1), { op: "ping" });
  assert.deepEqual(events, [{ event: "auth", ok: true }]);

  media.close();
  media.close();
  assert.equal(pc.closed, true);
  assert.equal(pc.channel.readyState, "closed");
  assert.equal(media.isAuthenticated, false);
});

test("VoiceMediaClient: a rejected credential fails ready", async () => {
  let pc;
  const media = new VoiceMediaClient({
    createPeerConnection: (config) => (pc = new FakePeerConnection(config)),
    exchange: async () => connectResponse,
  });
  await media.connect();
  pc.channel.open();
  pc.channel.emit({ event: "auth", ok: false });
  await assert.rejects(media.ready, /rejected the media credential/);
  assert.equal(media.isAuthenticated, false);
});

test("VoiceMediaClient: exchange failure closes the connection and rethrows", async () => {
  let pc;
  const media = new VoiceMediaClient({
    createPeerConnection: (config) => (pc = new FakePeerConnection(config)),
    exchange: async () => { throw new Error("gateway down"); },
  });
  await assert.rejects(media.connect(), /gateway down/);
  assert.equal(pc.closed, true);
});

test("VoiceMediaClient: needs RTCPeerConnection or an injected factory", async () => {
  const media = new VoiceMediaClient({ exchange: async () => connectResponse });
  await assert.rejects(media.connect(), /RTCPeerConnection is not available/);
});
