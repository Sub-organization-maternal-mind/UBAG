<script lang="ts">
  import type { Workflow, WorkflowStep } from '$lib/api/types';

  let { workflow }: { workflow: Workflow } = $props();

  const STATUS_COLORS: Record<string, string> = {
    completed: '#50a082', // success green
    running: '#366290',   // marine
    pending: '#b08840',   // saffron
    failed: '#b04040',    // danger red
  };

  // Compute layers (topological sort)
  function computeLayers(steps: WorkflowStep[]): Map<string, number> {
    const stepMap = new Map(steps.map(s => [s.id, s]));
    const layers = new Map<string, number>();

    function getLayer(id: string): number {
      if (layers.has(id)) return layers.get(id)!;
      const step = stepMap.get(id);
      if (!step?.depends_on?.length) {
        layers.set(id, 0);
        return 0;
      }
      const maxDep = Math.max(...step.depends_on.map(d => getLayer(d)));
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
    width={svgWidth}
    height={svgHeight}
    viewBox="0 0 {svgWidth} {svgHeight}"
    aria-label="Workflow steps diagram for {workflow.name}"
    role="img"
  >
    <defs>
      <marker id="arrow" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto">
        <path d="M0,0 L0,6 L8,3 z" fill="#86868a" />
      </marker>
    </defs>

    <!-- Edges -->
    {#each edges as edge}
      <line
        x1={edge.x1} y1={edge.y1}
        x2={edge.x2} y2={edge.y2}
        stroke="#86868a"
        stroke-width="1.5"
        marker-end="url(#arrow)"
      />
    {/each}

    <!-- Nodes -->
    {#each nodes as node}
      {@const color = STATUS_COLORS[node.step.status ?? ''] ?? '#86868a'}
      <g transform="translate({node.x},{node.y})" role="listitem">
        <rect
          width={RECT_W}
          height={RECT_H}
          rx="6"
          fill="var(--color-paper-soft, #f9f9f6)"
          stroke={color}
          stroke-width="2"
        />
        <text
          x={RECT_W / 2}
          y={RECT_H / 2 - 6}
          text-anchor="middle"
          dominant-baseline="middle"
          font-size="12"
          font-family="Cascadia Mono, monospace"
          fill="var(--color-ink, #1a1a1a)"
        >{node.step.name}</text>
        <text
          x={RECT_W / 2}
          y={RECT_H / 2 + 10}
          text-anchor="middle"
          font-size="10"
          fill={color}
          font-family="monospace"
        >{node.step.status ?? 'pending'}</text>
      </g>
    {/each}
  </svg>
</div>
