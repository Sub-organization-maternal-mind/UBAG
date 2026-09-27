<script lang="ts">
  let { count = 4, cols = 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4' }: { count?: number; cols?: string } = $props();
</script>

<div class="grid gap-4 {cols}" role="status" aria-label="Loading">
  <span class="sr-only">Loading…</span>
  {#each Array(count) as _}
    <div class="card space-y-3" aria-hidden="true">
      <span class="skeleton block h-3 w-20"></span>
      <span class="skeleton block h-6 w-28"></span>
    </div>
  {/each}
</div>
