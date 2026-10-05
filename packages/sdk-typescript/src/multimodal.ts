import type { UbagChatContentPart } from "./types.js";

// Content-part builders for the OpenAI-compatible facade
// (POST /v1/openai/chat/completions). Parts are inline only: images are
// base64 data URLs (png/jpeg/webp/gif), audio is base64 wav/mp3; the gateway
// rejects remote media URLs. Per-target support: GET /v1/capabilities
// (`inline_message_parts`).

type Bytes = Uint8Array | ArrayBuffer;

export function textPart(text: string): UbagChatContentPart {
  return { type: "text", text };
}

/**
 * Image part from a `data:` URL, or from raw bytes plus their MIME type.
 * Anything that is not a data URL (for example https://...) throws: the
 * gateway never fetches remote media.
 */
export function imagePart(source: string | Bytes, mime?: string): UbagChatContentPart {
  if (typeof source === "string") {
    if (!source.startsWith("data:")) {
      throw new TypeError("imagePart: pass a data: URL or bytes with a MIME type; remote URLs are not supported.");
    }
    return { type: "image_url", image_url: { url: source } };
  }
  if (!mime) {
    throw new TypeError("imagePart: a MIME type (for example image/png) is required with bytes.");
  }
  return { type: "image_url", image_url: { url: `data:${mime};base64,${toBase64(source)}` } };
}

/** Audio part from raw bytes; `format` is "wav" or "mp3". */
export function audioPart(bytes: Bytes, format: "wav" | "mp3"): UbagChatContentPart {
  if (format !== "wav" && format !== "mp3") {
    throw new TypeError('audioPart: format must be "wav" or "mp3".');
  }
  return { type: "input_audio", input_audio: { data: toBase64(bytes), format } };
}

// btoa works in browsers and Node 16+; chunking avoids call-stack limits.
function toBase64(input: Bytes): string {
  const bytes = input instanceof Uint8Array ? input : new Uint8Array(input);
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
}
