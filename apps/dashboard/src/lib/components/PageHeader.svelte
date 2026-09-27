<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    subtitle = '',
    actions,
    children,
  }: {
    title: string;
    subtitle?: string;
    actions?: Snippet;
    children?: Snippet;
  } = $props();

  $effect(() => {
    document.title = `${title} · UBAG Dashboard`;
  });
</script>

<div class="flex flex-wrap items-start justify-between gap-3">
  <div class="min-w-0">
    <h1 class="text-2xl font-display font-bold text-ink">{title}</h1>
    {#if subtitle}
      <p class="mt-0.5 text-xs text-ink-mute max-w-prose [overflow-wrap:anywhere]">{subtitle}</p>
    {/if}
    {@render children?.()}
  </div>
  {#if actions}
    <div class="flex shrink-0 items-center gap-2 pt-1">
      {@render actions()}
    </div>
  {/if}
</div>
