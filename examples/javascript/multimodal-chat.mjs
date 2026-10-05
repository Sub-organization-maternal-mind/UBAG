// Multimodal chat: text + image + audio in one OpenAI-compatible request.
//   node examples/javascript/multimodal-chat.mjs [target] [image.png] [clip.wav]
// Without file arguments a 1x1 PNG and half a second of silence are used.
import { audioPart, bytesOrFallback, clientFromEnv, imagePart, silentWav, textPart, tinyPng, UbagApiError } from "./sdk.mjs";

const [target = "chatgpt_web", imagePath, audioPath] = process.argv.slice(2);
const client = clientFromEnv();

// Check the target accepts these parts first; the gateway rejects unsupported ones at create time.
const capability = (await client.listCapabilities()).data.find((t) => t.target === target);
if (!capability) throw new Error(`Unknown target ${target}`);
if (!capability.inline_message_parts.image_url.includes("image/png")) console.warn(`${target} does not accept inline PNG images.`);
if (!capability.inline_message_parts.input_audio.includes("audio/wav")) console.warn(`${target} does not accept inline WAV audio.`);

try {
  const reply = await client.createChatCompletion({
    model: target,
    messages: [
      {
        role: "user",
        content: [
          textPart("Describe the image, then transcribe the audio clip."),
          imagePart(bytesOrFallback(imagePath, tinyPng), "image/png"),
          audioPart(bytesOrFallback(audioPath, silentWav), "wav"),
        ],
      },
    ],
  });
  console.log(reply.choices[0].message.content);
} catch (error) {
  // Facade errors are OpenAI-shaped: error.body.error.{message,type,code}.
  if (error instanceof UbagApiError) console.error(error.status, JSON.stringify(error.body), "retry after ms:", error.retryAfterMs);
  throw error;
}
