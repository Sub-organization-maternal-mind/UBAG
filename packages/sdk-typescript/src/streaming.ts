const TERMINAL_TYPES = new Set(["completed", "failed", "cancelled", "dead_letter"]);

export interface SseEvent {
  type: string;
  sequence?: number;
  [key: string]: unknown;
}

// parseSseChunk extracts JSON events from SSE `data:` lines in a text chunk.
export function parseSseChunk(chunk: string): SseEvent[] {
  const events: SseEvent[] = [];
  for (const block of chunk.split("\n\n")) {
    const line = block.split("\n").find((l) => l.startsWith("data:"));
    if (!line) continue;
    const json = line.slice("data:".length).trim();
    if (!json) continue;
    try {
      events.push(JSON.parse(json) as SseEvent);
    } catch {
      // skip malformed frame
    }
  }
  return events;
}

export function isTerminalEvent(event: { type: string }): boolean {
  return TERMINAL_TYPES.has(event.type);
}

// streamEvents consumes a ReadableStream of SSE bytes and yields events,
// stopping after the first terminal event.
export async function* streamEvents(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<SseEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const splitAt = buffer.lastIndexOf("\n\n");
      if (splitAt === -1) continue;
      const ready = buffer.slice(0, splitAt + 2);
      buffer = buffer.slice(splitAt + 2);
      for (const event of parseSseChunk(ready)) {
        yield event;
        if (isTerminalEvent(event)) return;
      }
    }
    for (const event of parseSseChunk(buffer)) {
      yield event;
      if (isTerminalEvent(event)) return;
    }
  } finally {
    reader.releaseLock();
  }
}
