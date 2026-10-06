import test from 'node:test';
import assert from 'node:assert/strict';
import { deriveReadyCandidates, pythonTuple, safeText, selectorFor } from './voice-probe-lib.mjs';

const c = (aria_label, extra = {}) => ({ tag: 'button', aria_label, testid: null, visible: true, ...extra });

test('only new, stable, non-entry controls become candidates', () => {
  const baseline = [c('Dictate'), c('Start Voice')];
  const inCall = [c('Dictate'), c('Start Voice'), c('End voice mode'), c('Mute microphone'),
    c(null, { text: 'x' }), c('Hidden', { visible: false }), c('End voice mode')];
  const got = deriveReadyCandidates(baseline, inCall);
  assert.deepEqual(got.map((g) => g.selector), ["[aria-label='End voice mode']", "[aria-label='Mute microphone']"]);
});

test('testid wins and quotes are escaped; entry/dictate never offered', () => {
  assert.equal(selectorFor(c("it's", { testid: 'end-call' })), "[data-testid='end-call']");
  assert.equal(selectorFor(c("it's")), "[aria-label='it\\'s']");
  assert.deepEqual(deriveReadyCandidates([], [c('Start Voice'), c('Dictate (x)'), c('Listen')]), []);
});

test('python tuple and text redaction', () => {
  assert.equal(pythonTuple([]), '()');
  assert.match(pythonTuple(["[a='b']"]), /^\(\n {4}"\[a='b'\]",\n\)$/);
  assert.equal(safeText('Speaking PRs #319/#320 diverged. Reconcile them safely.'), '');
  assert.equal(safeText('End'), 'End');
});
