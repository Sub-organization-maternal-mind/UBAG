<script lang="ts">
  import { page } from '$app/stores';
  import { base } from '$app/paths';
</script>

<div class="flex flex-col items-center justify-center py-20 text-center">
  <p class="font-mono text-xs uppercase tracking-widest text-ink-mute">{$page.status}</p>
  <h1 class="mt-2 text-2xl font-display font-bold text-ink">
    {$page.status === 404 ? 'Page not found' : 'Something went wrong'}
  </h1>
  <p class="mt-2 max-w-prose text-sm text-ink-soft">
    {#if $page.status === 404}
      The page <code class="font-mono text-xs">{$page.url.pathname}</code> does not exist on this dashboard.
    {:else}
      An unexpected error occurred while rendering this page.
      {#if $page.error?.message}
        <span class="block mt-1 font-mono text-xs text-danger">{$page.error.message}</span>
      {/if}
    {/if}
  </p>
  <div class="mt-6 flex items-center gap-3">
    <a href={base + '/'} class="btn btn-primary">Back to Overview</a>
    <button type="button" class="btn btn-secondary" onclick={() => location.reload()}>Reload</button>
  </div>
</div>
