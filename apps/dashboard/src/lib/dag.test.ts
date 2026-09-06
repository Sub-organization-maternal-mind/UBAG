import { describe, expect, it } from 'vitest';

import {
  assignStepIds,
  findCycle,
  toCreateSteps,
  validateDraft,
  type DagDraftStep,
} from './dag.js';

function step(key: string, deps: string[] = [], extra: Partial<DagDraftStep> = {}): DagDraftStep {
  return { key, target: 'chatgpt_web', command: 'submit', prompt: 'hi', deps, ...extra };
}

describe('assignStepIds', () => {
  it('maps keys to step_1..N in list order', () => {
    const ids = assignStepIds([step('a'), step('b'), step('c')]);
    expect([...ids.entries()]).toEqual([['a', 'step_1'], ['b', 'step_2'], ['c', 'step_3']]);
  });
});

describe('findCycle', () => {
  it('returns null for linear and diamond graphs', () => {
    expect(findCycle([step('a'), step('b', ['a']), step('c', ['b'])])).toBeNull();
    expect(findCycle([step('a'), step('b', ['a']), step('c', ['a']), step('d', ['b', 'c'])])).toBeNull();
  });

  it('returns null for empty input', () => {
    expect(findCycle([])).toBeNull();
  });

  it('detects a two-node cycle', () => {
    const cycle = findCycle([step('a', ['b']), step('b', ['a'])]);
    expect(cycle).not.toBeNull();
    expect(cycle![0]).toBe(cycle![cycle!.length - 1]);
  });

  it('detects a self dependency', () => {
    expect(findCycle([step('a', ['a'])])).toEqual(['a', 'a']);
  });

  it('ignores dangling dep keys (validated separately)', () => {
    expect(findCycle([step('a', ['ghost'])])).toBeNull();
  });
});

describe('validateDraft', () => {
  it('requires a name and at least one step', () => {
    expect(validateDraft('', [step('a')])).toMatch(/name/i);
    expect(validateDraft('w', [])).toMatch(/step/i);
  });

  it('requires target and command per step', () => {
    expect(validateDraft('w', [step('a', [], { target: '' })])).toMatch(/target/i);
    expect(validateDraft('w', [step('a', [], { command: '' })])).toMatch(/command/i);
  });

  it('rejects self-dependency, unknown deps, and cycles', () => {
    expect(validateDraft('w', [step('a', ['a'])])).toMatch(/itself/i);
    expect(validateDraft('w', [step('a', ['ghost'])])).toMatch(/unknown/i);
    expect(validateDraft('w', [step('a', ['b']), step('b', ['a'])])).toMatch(/cycle/i);
  });

  it('accepts a valid diamond', () => {
    const steps = [step('a'), step('b', ['a']), step('c', ['a']), step('d', ['b', 'c'])];
    expect(validateDraft('w', steps)).toBeNull();
  });
});

describe('toCreateSteps', () => {
  it('maps keys to ids and drops empty depends_on', () => {
    const out = toCreateSteps([step('a'), step('b', ['a'])]);
    expect(out).toEqual([
      { id: 'step_1', target: 'chatgpt_web', command: 'submit', input: { prompt: 'hi' } },
      { id: 'step_2', target: 'chatgpt_web', command: 'submit', input: { prompt: 'hi' }, depends_on: ['step_1'] },
    ]);
    expect('depends_on' in out[0]).toBe(false);
  });

  it('dedupes repeated dependencies', () => {
    const out = toCreateSteps([step('a'), step('b', ['a', 'a'])]);
    expect(out[1].depends_on).toEqual(['step_1']);
  });
});
