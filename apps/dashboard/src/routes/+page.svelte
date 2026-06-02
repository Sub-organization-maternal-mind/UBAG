<script lang="ts">
  import EmptyState from '$lib/components/EmptyState.svelte';
</script>

<div class="space-y-4">
  <h1 class="text-2xl font-display font-bold text-ink">Overview</h1>
  <p class="text-ink-soft text-sm">Gateway summary and live metrics will appear here.</p>
  <EmptyState message="No data yet." hint="Connect a gateway in Settings to get started." />
</div>
