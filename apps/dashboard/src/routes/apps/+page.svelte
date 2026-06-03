<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';

  type AppItem = Record<string, unknown>;

  let items = $state<AppItem[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');

  let filtered = $derived(
    filter
      ? items.filter((a) => JSON.stringify(a).toLowerCase().includes(filter.toLowerCase()))
      : items
  );

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/apps');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<AppItem>(res);
  }

  function str(v: unknown): string {
    if (v == null) return '—';
    if (typeof v === 'object') return JSON.stringify(v);
    return String(v);
  }

  onMount(load);
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Apps</h1>
    <button onclick={load} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <input
    type="search"
    bind:value={filter}
    placeholder="Filter by name, version…"
    class="w-full max-w-sm px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
  />

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="apps" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No apps registered." hint={filter ? 'Try clearing the filter.' : 'Register an app via the gateway API to get an app secret.'} />
  {:else}
    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Name</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Version</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as app, i (app['id'] ?? i)}
            <tr class="hover:bg-paper-soft transition-colors">
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{str(app['id']).slice(0, 8)}…</td>
              <td class="px-4 py-2.5 text-ink font-medium">{str(app['name'])}</td>
              <td class="px-4 py-2.5 text-ink-soft font-mono text-xs">{str(app['version'])}</td>
              <td class="px-4 py-2.5"><StatusBadge status={str(app['status'])} /></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
