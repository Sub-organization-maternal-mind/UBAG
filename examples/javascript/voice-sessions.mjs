// Backend voice-session management (no browser): create, wait/queue handling,
// lease heartbeat, mute and cleanup. Media (WebRTC) is the client's job, see
// voice-browser.html + voice-backend.mjs.   node examples/javascript/voice-sessions.mjs [target]
import { clientFromEnv, UbagApiError } from "./sdk.mjs";

const target = process.argv[2] ?? "chatgpt_web";
const client = clientFromEnv();

let created;
try {
  created = await client.createVoiceSession({ target }); // mode defaults to "live"
} catch (error) {
  // 429 = per-tenant session or queue budget reached; wait for Retry-After and try again.
  if (error instanceof UbagApiError && error.status === 429) {
    console.error(`Voice budget reached (${error.code}); retry in ${error.retryAfterMs} ms.`);
    process.exit(1);
  }
  throw error;
}
console.log(`${created.status} session ${created.session_id}`); // connecting (201) or queued (202)

// A queued session holds no provider account yet; connect (from the client) claims one when free.
// Status values: queued | connecting | connected | terminated. "connected" only appears once the
// provider voice UI is verified ready AND the client media is up; failures terminate the session
// with last_error such as "activation_failed: <state>".

// While a client is in a call, keep the lease alive; a lapsed lease frees the provider account.
const heartbeat = setInterval(() => client.renewVoiceSessionLease(created.session_id).catch((e) => console.error("renew:", e.message)), 60_000);

try {
  const { session } = await client.getVoiceSession(created.session_id);
  console.log("status:", session.status, session.last_error ?? "");
  await client.muteVoiceSession(created.session_id, true); // mic off; the provider can still speak
  const { data } = await client.listVoiceSessions({ target, limit: 10 });
  console.log("my sessions:", data.map((s) => `${s.session_id}:${s.status}`).join(", "));
} finally {
  clearInterval(heartbeat);
  await client.terminateVoiceSession(created.session_id); // always release the account and media path
}
