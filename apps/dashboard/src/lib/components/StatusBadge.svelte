<script lang="ts">
  let { status }: { status: string } = $props();

  const tone: Record<string, string> = {
    running: 'bg-marine-soft text-marine',
    active: 'bg-marine-soft text-marine',
    pending: 'bg-saffron-soft text-saffron',
    queued: 'bg-saffron-soft text-saffron',
    completed: 'bg-success-soft text-success',
    done: 'bg-success-soft text-success',
    failed: 'bg-danger-soft text-danger',
    error: 'bg-danger-soft text-danger',
    cancelled: 'bg-rule-soft text-ink-mute',
  };

  let cls = $derived(tone[status?.toLowerCase()] ?? 'bg-rule-soft text-ink-mute');
</script>

<span class="inline-flex items-center px-2 py-0.5 rounded-pill text-xs font-mono font-semibold {cls}">
  {status ?? 'unknown'}
</span>
