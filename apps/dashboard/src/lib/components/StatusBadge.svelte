<script lang="ts">
  let { status }: { status: string } = $props();

  // Keys are real gateway job statuses (contract vocabulary). The fallback
  // tone covers non-job statuses (alerts, contexts) without inventing words.
  const tone: Record<string, string> = {
    created: 'bg-rule-soft text-ink-mute',
    scheduled: 'bg-saffron-soft text-saffron',
    queued: 'bg-saffron-soft text-saffron',
    assigned: 'bg-marine-soft text-marine',
    running: 'bg-marine-soft text-marine',
    token_streaming: 'bg-marine-soft text-marine',
    completing: 'bg-marine-soft text-marine',
    completed: 'bg-success-soft text-success',
    completed_with_warnings: 'bg-saffron-soft text-saffron',
    failed_retryable: 'bg-danger-soft text-danger',
    failed_terminal: 'bg-danger-soft text-danger',
    dead_letter: 'bg-danger-soft text-danger',
    cancelled: 'bg-rule-soft text-ink-mute',
    timed_out: 'bg-danger-soft text-danger',
  };

  let cls = $derived(tone[status?.toLowerCase()] ?? 'bg-rule-soft text-ink-mute');
</script>

<span class="inline-flex items-center px-2 py-0.5 rounded-pill text-xs font-mono font-semibold {cls}">
  {status ?? 'unknown'}
</span>
