<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import type { Target } from '$lib/api/types';

  let items = $state<Target[]>([]);
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
    return items.filter((t) =>
      `${t.key ?? ''} ${t.display_name ?? ''} ${t.adapter_key ?? ''}`.toLowerCase().includes(q)
    );
  });

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/targets');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<Target>(res);
  }

  onMount(load);
</script>

<div class="space-y-4">
  <PageHeader title="Targets" subtitle="Registered provider targets and their login/safe-mode posture.">
    {#snippet actions()}
      <button onclick={load} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <input
    type="search"
    value={filter}
    oninput={onFilterInput}
    placeholder="Filter by key, name, adapter…"
    class="input max-w-sm"
  />

  {#if loading}
    <SkeletonTable rows={6} cols={5} />
  {:else if denied}
    <DeniedPanel resource="targets" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No targets found." hint={filter ? 'Try clearing the filter.' : 'Register a target via the gateway API.'} />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">Key</th>
            <th class="th">Name</th>
            <th class="th">Adapter</th>
            <th class="th">Manual Login</th>
            <th class="th">Safe Mode</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as target (target.key)}
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink-mute">{target.key}</td>
              <td class="td font-medium text-ink">{target.display_name}</td>
              <td class="td font-mono text-xs">{target.adapter_key}</td>
              <td class="td">
                {#if target.manual_login_required}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-warning-soft text-warning">Yes</span>
                {:else}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-paper-soft text-ink-mute">No</span>
                {/if}
              </td>
              <td class="td">
                {#if target.safe_mode}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success-soft text-success">Yes</span>
                {:else}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-paper-soft text-ink-mute">No</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
