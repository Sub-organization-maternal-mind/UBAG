<script lang="ts">
  let { message = 'An error occurred.', retry = undefined }: { message?: string; retry?: () => void } = $props();
</script>

<div class="rounded-md border border-rule bg-paper-soft p-5 text-sm" role="alert" aria-label="Error">
  <div class="flex items-start gap-3">
    <svg class="w-5 h-5 text-ink-mute shrink-0 mt-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true">
      <circle cx="12" cy="12" r="10" stroke-width="2"/>
      <path stroke-linecap="round" stroke-width="2" d="M12 8v4m0 4h.01"/>
    </svg>
    <div class="flex-1">
      <p class="font-medium text-ink">Error</p>
      <p class="text-ink-mute mt-1">{message}</p>
      {#if retry}
        <button onclick={retry} class="mt-3 text-accent-deep hover:underline font-medium">Retry</button>
      {/if}
    </div>
  </div>
</div>
