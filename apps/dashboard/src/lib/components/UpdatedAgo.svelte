<script lang="ts">
  let { at }: { at: Date | null } = $props();
  let now = $state(Date.now());
  let timer: ReturnType<typeof setInterval> | undefined;

  $effect(() => {
    if (at === null) return;
    now = Date.now();
    const tick = () => {
      if (!document.hidden) now = Date.now();
    };
    timer = setInterval(tick, 10_000);
    return () => clearInterval(timer);
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
