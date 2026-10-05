// Capability discovery: what each target accepts and whether live voice can
// actually start now.   node examples/javascript/capabilities.mjs
import { clientFromEnv } from "./sdk.mjs";

const client = clientFromEnv();
const { data } = await client.listCapabilities();

for (const target of data) {
  const { voice, inline_message_parts: parts } = target;
  console.log(`${target.target}`);
  console.log(`  images: ${parts.image_url.join(", ") || "none"}   audio: ${parts.input_audio.join(", ") || "none"}`);
  // supported = adapter declares live voice; configured = this gateway can serve it;
  // verified = a live acceptance run is recorded; available = a free account exists now.
  console.log(
    `  voice: supported=${voice.supported} configured=${voice.configured} verified=${voice.verified} ` +
      `available=${voice.available} free=${voice.free_resources} utterance_jobs=${voice.utterance_jobs}`,
  );
}

const liveReady = data.filter((t) => t.voice.available);
console.log(liveReady.length ? `Live voice can start now on: ${liveReady.map((t) => t.target).join(", ")}` : "No target can start live voice right now.");
