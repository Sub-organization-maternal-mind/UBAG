<script lang="ts">
  let { message = 'No items found.', hint = '' }: { message?: string; hint?: string } = $props();
</script>

<div class="flex flex-col items-center justify-center py-16 text-center text-sm" aria-live="polite">
  <div class="w-14 h-14 mb-4 rounded-md bg-paper-warm border border-rule-soft flex items-center justify-center" aria-hidden="true">
    <svg class="w-6 h-6 text-ink-mute" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M20 13V6a2 2 0 00-2-2H6a2 2 0 00-2 2v7m16 0v5a2 2 0 01-2 2H6a2 2 0 01-2-2v-5m16 0H4" />
    </svg>
  </div>
  <p class="font-medium text-ink">{message}</p>
  {#if hint}
    <p class="text-ink-mute mt-1">{hint}</p>
  {/if}
</div>
