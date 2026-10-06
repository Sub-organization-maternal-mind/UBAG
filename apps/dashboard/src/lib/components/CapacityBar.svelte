<script lang="ts">
  import type { Snippet } from 'svelte';
  import { barColor, capacityPct } from '$lib/api/fleet';

  // Usage card: a name, used / limit, and a bar that turns warning at 70% and
  // danger at 90%. Extra lines (grant details, state) go in as children.
  let {
    name,
    used,
    limit,
    unit = '',
    children,
  }: { name: string; used: number; limit: number; unit?: string; children?: Snippet } = $props();

  let p = $derived(capacityPct(used, limit));
</script>

<div class="card space-y-2">
  <div class="flex items-baseline justify-between gap-2">
    <p class="text-sm font-medium text-ink truncate" title={name}>{name}</p>
    <p class="text-xs text-ink-mute shrink-0">{p}%</p>
  </div>
  <div class="h-2 rounded-full bg-rule overflow-hidden">
    <div
      class="h-full rounded-full transition-all duration-500 ease-out {barColor(p)}"
      style="width: {p}%"
      role="progressbar"
      aria-label="{name} usage"
      aria-valuenow={used}
      aria-valuemin={0}
      aria-valuemax={limit}
    ></div>
  </div>
  <p class="text-xs text-ink-mute">
    {used.toLocaleString()} / {limit.toLocaleString()}{unit ? ' ' + unit : ''}
  </p>
  {@render children?.()}
</div>
