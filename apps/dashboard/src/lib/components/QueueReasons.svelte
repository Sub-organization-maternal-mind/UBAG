<script lang="ts">
  import { queueReasonRows } from '$lib/api/fleet';

  // Queued jobs counted by reason. The reason set is open, so unknown keys show as
  // they are; `hints` adds the one-line meaning of each coarse reason.
  let {
    title,
    counts,
    hints = false,
    empty = 'Nothing waiting.',
    note = '',
  }: {
    title: string;
    counts: Record<string, number>;
    hints?: boolean;
    /** Shown instead of the list while nothing is counted. */
    empty?: string;
    note?: string;
  } = $props();

  let rows = $derived(queueReasonRows(counts));
</script>

<section class="card space-y-2" aria-label={title}>
  <h2 class="text-sm font-semibold text-ink uppercase tracking-wider">{title}</h2>
  {#if rows.length === 0}
    <p class="text-xs text-ink-mute italic">{empty}</p>
  {:else}
    <ul class="divide-y divide-rule text-sm">
      {#each rows as row (row.reason)}
        <li class="flex items-baseline justify-between gap-3 py-1.5">
          <span class="min-w-0">
            <span class="text-ink">{row.label}</span>
            {#if hints && row.hint}
              <span class="block text-xs text-ink-mute">{row.hint}</span>
            {/if}
          </span>
          <span class="shrink-0 font-mono text-xs text-ink">{row.count.toLocaleString()}</span>
        </li>
      {/each}
    </ul>
  {/if}
  {#if note}
    <p class="text-xs text-ink-mute">{note}</p>
  {/if}
</section>
