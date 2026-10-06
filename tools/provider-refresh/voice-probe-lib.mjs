// Pure helpers for the in-call voice probe (voice-probe.mjs --in-call).
// No CDP, no DOM, no clicks: they only diff two read-only captures and propose
// CANDIDATE VoiceReadiness.ready_controls selectors for a human to review.
// A candidate is never evidence; selectors.py is only edited by a human after a
// supervised two-way demo (see docs/perf-fleet/voice-activation-probe.md).

// Controls that are never offered as ready candidates: they exist before a call,
// or are speech-to-text only ("Dictate"), or are the entry control itself.
const NEVER_READY = /(dictate|start voice|^listen$|attach|upload|send|new chat|sidebar|menu|settings)/i;

const esc = (v) => String(v).replace(/\\/g, '\\\\').replace(/'/g, "\\'");

/** Stable CSS selector for a described control, or null when only unstable hints exist. */
export function selectorFor(d) {
  if (d.testid) return `[data-testid='${esc(d.testid)}']`;
  if (d.aria_label) return `[aria-label='${esc(d.aria_label)}']`;
  return null; // text/position are not stable enough to ship
}

const keyOf = (d) => `${d.tag}|${d.testid || ''}|${d.aria_label || ''}`;

/**
 * Controls visible during the call that were NOT visible in the pre-call baseline.
 * Returns [{selector, tag, aria_label, testid}] (deduplicated).
 */
export function deriveReadyCandidates(baseline = [], inCall = []) {
  const before = new Set(baseline.filter((d) => d.visible).map(keyOf));
  const out = [];
  const seen = new Set();
  for (const d of inCall) {
    if (!d.visible) continue;
    const sel = selectorFor(d);
    if (!sel || before.has(keyOf(d)) || seen.has(sel)) continue;
    if (NEVER_READY.test([d.aria_label, d.testid].filter(Boolean).join(' '))) continue;
    seen.add(sel);
    out.push({ selector: sel, tag: d.tag, aria_label: d.aria_label, testid: d.testid });
  }
  return out;
}

/** Python tuple literal for a human to paste into VoiceReadiness(ready_controls=...). */
export function pythonTuple(selectors) {
  if (!selectors.length) return '()';
  return `(\n${selectors.map((s) => `    ${JSON.stringify(s)},`).join('\n')}\n)`;
}

// Free text can carry conversation content; keep only short label-like text.
export const safeText = (t) => (t && t.length <= 24 ? t : '');
