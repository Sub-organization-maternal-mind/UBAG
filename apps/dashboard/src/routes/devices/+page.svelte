<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';

  type DeviceItem = Record<string, unknown>;

  let items = $state<DeviceItem[]>([]);
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
    return items.filter((d) =>
      `${str(d['id'])} ${str(d['name'])} ${str(d['type'])} ${str(d['status'])}`
        .toLowerCase()
        .includes(q)
    );
  });

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/devices');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<DeviceItem>(res);
  }

  function str(v: unknown): string {
    if (v == null) return '—';
    if (typeof v === 'object') return JSON.stringify(v);
    return String(v);
  }

  onMount(load);
</script>

<div class="space-y-4">
  <PageHeader title="Devices" subtitle="Device inventory registered with the gateway.">
    {#snippet actions()}
      <button onclick={load} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <input
    type="search"
    value={filter}
    oninput={onFilterInput}
    placeholder="Filter by name, type…"
    class="input max-w-sm"
  />

  {#if loading}
    <SkeletonTable rows={6} cols={4} />
  {:else if denied}
    <DeniedPanel resource="devices" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No devices found." hint={filter ? 'Try clearing the filter.' : 'Connect a device to the gateway to see it listed here.'} />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">ID</th>
            <th class="th">Name</th>
            <th class="th">Type</th>
            <th class="th">Status</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as device, i (device['id'] ?? i)}
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink-mute">{str(device['id']).slice(0, 8)}…</td>
              <td class="td font-medium text-ink">{str(device['name'])}</td>
              <td class="td">{str(device['type'])}</td>
              <td class="td"><StatusBadge status={str(device['status'])} /></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
