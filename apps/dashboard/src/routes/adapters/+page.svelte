<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import type { Adapter } from '$lib/api/types';

  let items = $state<Adapter[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');
  let filterQuery = $state('');
  let filterTimer: ReturnType<typeof setTimeout> | undefined;

  function onFilterInput(e: Event) {
    const value = (e.currentTarget as HTMLInputElement).value;
    filter = value;
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => (filterQuery = value), 120);
  }

  let filtered = $derived.by(() => {
    const q = filterQuery.trim().toLowerCase();
    if (!q) return items;
    return items.filter((a) =>
      `${a.key ?? ''} ${a.kind ?? ''} ${a.stage ?? ''} ${(a.capabilities ?? []).join(' ')}`
        .toLowerCase()
        .includes(q)
    );
  });

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/adapters');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<Adapter>(res);
  }

  onMount(load);
</script>

<div class="space-y-4">
  <PageHeader title="Adapters" subtitle="Provider adapter registry — kind, lifecycle stage and capabilities.">
    {#snippet actions()}
      <button onclick={load} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <input
    type="search"
    value={filter}
    oninput={onFilterInput}
    placeholder="Filter by key, kind, stage…"
    class="input max-w-sm"
  />

  {#if loading}
    <SkeletonTable rows={6} cols={4} />
  {:else if denied}
    <DeniedPanel resource="adapters" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No adapters found." hint={filter ? 'Try clearing the filter.' : 'Install adapters via the gateway configuration.'} />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">Key</th>
            <th class="th">Kind</th>
            <th class="th">Stage</th>
            <th class="th">Capabilities</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as adapter (adapter.key)}
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink-mute">{adapter.key}</td>
              <td class="td font-medium text-ink">{adapter.kind}</td>
              <td class="td">
                <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-paper-soft border border-rule text-ink-soft font-mono">{adapter.stage}</span>
              </td>
              <td class="td text-xs">{adapter.capabilities?.join(', ') ?? '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
