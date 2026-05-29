<script lang="ts">
  // Inline async-state surface: loading spinner, error notice, or empty hint.
  export let loading = false;
  export let error: string | null = null;
  export let empty = false;
  export let emptyTitle = "Nothing here yet";
  export let emptyDetail = "No records were returned by the gateway.";
  export let onRetry: (() => void) | undefined = undefined;
</script>

{#if loading}
  <div class="notice" role="status" aria-live="polite" aria-busy="true">
    <span class="spinner" aria-hidden="true"></span>
    <div>
      <h3>Loading…</h3>
      <p>Contacting the gateway.</p>
    </div>
  </div>
{:else if error}
  <div class="notice" data-tone="danger" role="alert" aria-live="assertive">
    <span class="dot" data-tone="danger" aria-hidden="true"></span>
    <div>
      <h3>Could not load</h3>
      <p>{error}</p>
      {#if onRetry}
        <button class="btn secondary" style="margin-top: var(--space-2)" on:click={onRetry}>
          Retry
        </button>
      {/if}
    </div>
  </div>
{:else if empty}
  <div class="empty">
    <div class="pattern" aria-hidden="true"></div>
    <h3>{emptyTitle}</h3>
    <p class="muted">{emptyDetail}</p>
  </div>
{/if}
