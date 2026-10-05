<script lang="ts">
  import { pollWhileVisible } from '$lib/poll';
  let { at }: { at: Date | null } = $props();
  let now = $state(Date.now());

  $effect(() => {
    if (at === null) return;
    now = Date.now();
    // Visibility-aware: no timer while hidden, refresh on becoming visible.
    return pollWhileVisible(() => { now = Date.now(); }, 10_000, { immediate: false });
  });

  const label = $derived.by(() => {
    if (!at) return '';
    const s = Math.max(0, Math.round((now - at.getTime()) / 1000));
    if (s < 5) return 'just now';
    if (s < 90) return `${s}s ago`;
    const m = Math.round(s / 60);
    if (m < 90) return `${m}m ago`;
    return `${Math.round(m / 60)}h ago`;
  });
</script>

{#if at}
  <span class="font-mono text-xs text-ink-mute" title={at.toLocaleTimeString()}>updated {label}</span>
{/if}
