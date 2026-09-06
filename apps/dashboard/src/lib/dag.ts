// Pure DAG helpers for the workflow composer (unit-tested in dag.test.ts).
// Draft steps carry stable client-side keys; submit ids (step_1..N) are
// assigned in list order so dependencies survive reordering.

export interface DagDraftStep {
  key: string;
  target: string;
  command: string;
  prompt: string;
  deps: string[];
}

export interface DagCreateStep {
  id: string;
  target: string;
  command: string;
  input: { prompt: string };
  depends_on?: string[];
}

/** Map draft keys to submit ids (step_1..N in list order). */
export function assignStepIds(steps: DagDraftStep[]): Map<string, string> {
  return new Map(steps.map((s, i) => [s.key, `step_${i + 1}`]));
}

/** Depth-first cycle detection over key -> dep-keys. Returns a cycle path or null. */
export function findCycle(steps: DagDraftStep[]): string[] | null {
  const ids = new Map(steps.map((s) => [s.key, s]));
  const color = new Map<string, number>(); // 1 = gray (on stack), 2 = black (done)
  const stack: string[] = [];
  const visit = (key: string): string[] | null => {
    color.set(key, 1);
    stack.push(key);
    for (const dep of ids.get(key)?.deps ?? []) {
      if (!ids.has(dep)) continue;
      if (color.get(dep) === 1) return [...stack.slice(stack.indexOf(dep)), dep];
      if (!color.has(dep)) {
        const found = visit(dep);
        if (found) return found;
      }
    }
    stack.pop();
    color.set(key, 2);
    return null;
  };
  for (const s of steps) {
    if (!color.has(s.key)) {
      const found = visit(s.key);
      if (found) return found;
    }
  }
  return null;
}

/** Validate a draft; returns an error message or null when submittable. */
export function validateDraft(name: string, steps: DagDraftStep[]): string | null {
  if (!name.trim()) return 'Workflow name is required.';
  if (steps.length === 0) return 'Add at least one step.';
  for (const [i, s] of steps.entries()) {
    if (!s.target.trim()) return `Step ${i + 1}: target is required.`;
    if (!s.command.trim()) return `Step ${i + 1}: command is required.`;
    for (const d of s.deps) {
      if (d === s.key) return `Step ${i + 1}: a step cannot depend on itself.`;
      if (!steps.some((o) => o.key === d)) return `Step ${i + 1}: unknown dependency.`;
    }
  }
  const cycle = findCycle(steps);
  if (cycle) return `Dependency cycle: ${cycle.join(' → ')}.`;
  return null;
}

/** Map a validated draft to the POST /v1/workflows step payload. */
export function toCreateSteps(steps: DagDraftStep[]): DagCreateStep[] {
  const ids = assignStepIds(steps);
  return steps.map((s) => {
    const step: DagCreateStep = {
      id: ids.get(s.key) ?? s.key,
      target: s.target.trim(),
      command: s.command.trim(),
      input: { prompt: s.prompt },
    };
    const deps = s.deps
      .map((d) => ids.get(d) ?? d)
      .filter((dep, index, arr) => arr.indexOf(dep) === index);
    if (deps.length > 0) step.depends_on = deps;
    return step;
  });
}
