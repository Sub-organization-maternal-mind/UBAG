<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import type { Target, ListResponse } from '$lib/api/types';

  let items = $state<Target[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');

  let filtered = $derived(
    filter
      ? items.filter((t) => JSON.stringify(t).toLowerCase().includes(filter.toLowerCase()))
      : items
  );

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get<ListResponse<Target>>('/v1/targets');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    // Support both {items:[]} and {targets:[]} shapes
    const data = res.data as Record<string, unknown> | null;
    items = (data?.['items'] ?? data?.['targets'] ?? []) as Target[];
  }

  onMount(load);
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Targets</h1>
    <button onclick={load} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <input
    type="search"
    bind:value={filter}
    placeholder="Filter by name, URL, adapter…"
    class="w-full max-w-sm px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
  />

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="targets" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No targets found." hint={filter ? 'Try clearing the filter.' : 'Register a target via the gateway API.'} />
  {:else}
    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Name</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">URL</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Adapter</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as target (target.id)}
            <tr class="hover:bg-paper-soft transition-colors">
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{target.id.slice(0, 8)}…</td>
              <td class="px-4 py-2.5 text-ink font-medium">{target.name}</td>
              <td class="px-4 py-2.5 text-ink-soft font-mono text-xs truncate max-w-xs">{target.url}</td>
              <td class="px-4 py-2.5 text-ink-soft">{target.adapter}</td>
              <td class="px-4 py-2.5"><StatusBadge status={target.status} /></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
