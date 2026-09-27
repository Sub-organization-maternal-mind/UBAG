<script lang="ts">
  import type { Workflow, WorkflowStep } from '$lib/api/types';

  let { workflow }: { workflow: Workflow } = $props();

  // Token-driven colors (must stay in sync with the legend on /workflows).
  const STATUS_COLORS: Record<string, string> = {
    completed: 'var(--color-success)',
    running: 'var(--color-marine)',
    pending: 'var(--color-warning)',
    failed: 'var(--color-danger)',
  };

  function statusColor(status?: string): string {
    return STATUS_COLORS[status ?? ''] ?? 'var(--color-ink-mute)';
  }

  // Compute layers (topological sort with cycle guard)
  function computeLayers(steps: WorkflowStep[]): Map<string, number> {
    const stepMap = new Map(steps.map(s => [s.id, s]));
    const layers = new Map<string, number>();
    const inProgress = new Set<string>(); // cycle detection

    function getLayer(id: string, depth = 0): number {
      if (depth > steps.length) return 0; // cycle guard: bail out
      if (layers.has(id)) return layers.get(id)!;
      if (inProgress.has(id)) return 0; // cycle detected — treat as root
      inProgress.add(id);
      const step = stepMap.get(id);
      if (!step?.depends_on?.length) {
        inProgress.delete(id);
        layers.set(id, 0);
        return 0;
      }
      const maxDep = Math.max(...step.depends_on.map(d => getLayer(d, depth + 1)));
      inProgress.delete(id);
      const layer = maxDep + 1;
      layers.set(id, layer);
      return layer;
    }

    steps.forEach(s => getLayer(s.id));
    return layers;
  }

  const RECT_W = 160;
  const RECT_H = 50;
  const COL_GAP = 220;
  const ROW_GAP = 80;
  const PAD = 20;
  const NAME_MAX = 22;

  function displayName(name: string | undefined): string {
    const n = name ?? '';
    return n.length > NAME_MAX ? n.slice(0, NAME_MAX - 1) + '…' : n;
  }

  type Node = { step: WorkflowStep; x: number; y: number; layer: number };

  let nodes = $derived.by(() => {
    const steps = workflow.steps ?? [];
    const layers = computeLayers(steps);

    // Group steps by layer
    const byLayer = new Map<number, WorkflowStep[]>();
    for (const step of steps) {
      const l = layers.get(step.id) ?? 0;
      if (!byLayer.has(l)) byLayer.set(l, []);
      byLayer.get(l)!.push(step);
    }

    const result: Node[] = [];
    for (const [layer, layerSteps] of [...byLayer.entries()].sort(([a], [b]) => a - b)) {
      layerSteps.forEach((step, i) => {
        result.push({
          step,
          x: PAD + layer * COL_GAP,
          y: PAD + i * ROW_GAP,
          layer,
        });
      });
    }
    return result;
  });

  let nodeMap = $derived(new Map(nodes.map(n => [n.step.id, n])));

  let edges = $derived.by(() => {
    const result: Array<{ x1: number; y1: number; x2: number; y2: number }> = [];
    for (const node of nodes) {
      for (const depId of (node.step.depends_on ?? [])) {
        const dep = nodeMap.get(depId);
        if (dep) {
          result.push({
            x1: dep.x + RECT_W / 2,
            y1: dep.y + RECT_H,
            x2: node.x + RECT_W / 2,
            y2: node.y,
          });
        }
      }
    }
    return result;
  });

  let svgWidth = $derived(nodes.length ? Math.max(...nodes.map(n => n.x + RECT_W + PAD)) : 400);
  let svgHeight = $derived(nodes.length ? Math.max(...nodes.map(n => n.y + RECT_H + PAD)) : 200);
</script>

<div class="overflow-x-auto rounded-md border border-rule bg-paper-soft p-2" aria-label="Workflow DAG">
  <svg
    viewBox="0 0 {svgWidth} {svgHeight}"
    width={svgWidth}
    height={svgHeight}
    class="h-auto w-full min-w-[560px]"
    aria-label="Workflow steps diagram for {workflow.name}"
    role="img"
  >
    <defs>
      <marker id="dag-arrow" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto">
        <path d="M0,0 L0,6 L8,3 z" fill="var(--color-ink-mute)" />
      </marker>
    </defs>

    <!-- Edges -->
    {#each edges as edge, i (i)}
      <line
        x1={edge.x1} y1={edge.y1}
        x2={edge.x2} y2={edge.y2}
        stroke="var(--color-ink-mute)"
        stroke-width="1.5"
        marker-end="url(#dag-arrow)"
      />
    {/each}

    <!-- Nodes -->
    {#each nodes as node (node.step.id)}
      {@const color = statusColor(node.step.status)}
      <g transform="translate({node.x},{node.y})" role="listitem">
        <rect
          width={RECT_W}
          height={RECT_H}
          rx="6"
          fill="var(--color-paper-soft)"
          stroke={color}
          stroke-width="2"
        />
        <text
          x={RECT_W / 2}
          y={RECT_H / 2 - 6}
          text-anchor="middle"
          dominant-baseline="middle"
          font-size="12"
          font-family="var(--font-mono)"
          fill="var(--color-ink)"
        >{displayName(node.step.name)}</text>
        <text
          x={RECT_W / 2}
          y={RECT_H / 2 + 10}
          text-anchor="middle"
          font-size="10"
          fill={color}
          font-family="var(--font-mono)"
        >{node.step.status ?? 'pending'}</text>
      </g>
    {/each}
  </svg>
</div>
