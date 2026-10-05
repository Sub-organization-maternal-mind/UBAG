// Utterance mode: audio in, text out. No live voice leases are held; the
// session is backed by a transcription-style job.
//   node examples/javascript/utterance.mjs [target] [clip.wav]
import { bytesOrFallback, clientFromEnv, silentWav } from "./sdk.mjs";

const [target = "chatgpt_web", audioPath] = process.argv.slice(2);
const client = clientFromEnv();

const created = await client.createVoiceSession({ target, mode: "utterance" });
console.log("session", created.session_id, "job", created.job_id);

// The job is held until the audio lands under the declared key.
await client.putJobArtifact(created.job_id, "utterance.wav", bytesOrFallback(audioPath, silentWav), { contentType: "audio/wav" });

const terminal = new Set(["completed", "completed_with_warnings", "failed_retryable", "failed_terminal", "dead_letter", "cancelled", "timed_out"]);
let job;
do {
  await new Promise((resolve) => setTimeout(resolve, 2000));
  job = await client.getJob(created.job_id);
  console.log("job status:", job.status);
} while (!terminal.has(job.status));

console.log(JSON.stringify(job.result, null, 2));
await client.terminateVoiceSession(created.session_id); // utterance sessions are cleaned up like any other
