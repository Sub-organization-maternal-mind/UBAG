<script lang="ts">
  let { status }: { status: string } = $props();

  // Keys are real gateway job statuses (contract vocabulary). The fallback
  // tone covers non-job statuses (alerts, contexts) without inventing words.
  // Text on saffron-soft uses `warning` (darker amber) so every badge keeps
  // ≥4.5:1 contrast on its soft background.
  const tone: Record<string, string> = {
    created: 'bg-rule-soft text-ink-mute',
    scheduled: 'bg-saffron-soft text-warning',
    queued: 'bg-saffron-soft text-warning',
    assigned: 'bg-marine-soft text-marine',
    running: 'bg-marine-soft text-marine',
    token_streaming: 'bg-marine-soft text-marine',
    completing: 'bg-marine-soft text-marine',
    completed: 'bg-success-soft text-success',
    completed_with_warnings: 'bg-saffron-soft text-warning',
    failed_retryable: 'bg-danger-soft text-danger',
    failed_terminal: 'bg-danger-soft text-danger',
    dead_letter: 'bg-danger-soft text-danger',
    cancelled: 'bg-rule-soft text-ink-mute',
    timed_out: 'bg-danger-soft text-danger',
    // Helper-node placement states (GET /v1/fleet/nodes).
    eligible: 'bg-success-soft text-success',
    ineligible: 'bg-danger-soft text-danger',
    draining: 'bg-saffron-soft text-warning',
    lost: 'bg-danger-soft text-danger',
    unknown_reservation: 'bg-saffron-soft text-warning',
    // Voice session states (queued reuses the job tone above).
    connecting: 'bg-marine-soft text-marine',
    connected: 'bg-success-soft text-success',
    terminated: 'bg-rule-soft text-ink-mute',
  };

  let cls = $derived(tone[status?.toLowerCase()] ?? 'bg-rule-soft text-ink-mute');
</script>

<span class="inline-flex items-center px-2 py-0.5 rounded-pill text-xs font-mono font-semibold {cls}">
  {status ?? 'unknown'}
</span>
