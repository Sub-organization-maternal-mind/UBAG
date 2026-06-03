<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import type { AuditEntry } from '$lib/api/types';

  let items = $state<AuditEntry[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filterActor = $state('');
  let filterAction = $state('');

  let filtered = $derived(() => {
    let result = items;
    if (filterActor) {
      result = result.filter(e => e.actor.toLowerCase().includes(filterActor.toLowerCase()));
    }
    if (filterAction) {
      result = result.filter(e => e.action.toLowerCase().includes(filterAction.toLowerCase()));
    }
    return result;
  });

  // Chain verification: items[i].prev_hash === items[i-1].hash
  function chainValid(index: number, filteredItems: AuditEntry[]): boolean | null {
    if (index === 0) return null; // first entry — no previous to compare
    const prev = filteredItems[index - 1];
    const cur = filteredItems[index];
    if (!cur.prev_hash || !prev.hash) return null; // missing hashes — can't verify
    return cur.prev_hash === prev.hash;
  }

  function fmtDate(s: string): string {
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  function shortHash(h?: string): string {
    if (!h) return '—';
    return h.slice(-8);
  }

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/audit');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<AuditEntry>(res);
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Audit Log</h1>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <!-- Filters -->
  <div class="flex gap-3 flex-wrap">
    <input
      type="search"
      bind:value={filterActor}
      placeholder="Filter by actor…"
      class="w-full max-w-xs px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
    />
    <input
      type="search"
      bind:value={filterAction}
      placeholder="Filter by action…"
      class="w-full max-w-xs px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
    />
  </div>

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="audit log" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    {@const filteredItems = filtered()}
    {#if filteredItems.length === 0}
      <EmptyState message="No audit entries found." hint={filterActor || filterAction ? 'Try clearing the filters.' : ''} />
    {:else}
      <!-- Chain integrity summary -->
      {@const chainIssues = filteredItems.filter((_, i) => chainValid(i, filteredItems) === false).length}
      {#if chainIssues > 0}
        <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger flex items-center gap-2" role="alert">
          <svg class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
          </svg>
          Chain integrity: {chainIssues} broken link{chainIssues !== 1 ? 's' : ''} detected.
        </div>
      {:else}
        <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success flex items-center gap-2" role="status">
          <svg class="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
          </svg>
          Chain integrity: all verified.
        </div>
      {/if}

      <div class="rounded-md border border-rule overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Timestamp</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Actor</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Action</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Resource</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Hash</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Prev Hash</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Chain</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each filteredItems as entry, i (entry.id)}
              {@const valid = chainValid(i, filteredItems)}
              <tr class="hover:bg-paper-soft transition-colors" class:bg-danger-soft={valid === false}>
                <td class="px-4 py-2.5 text-ink-mute text-xs whitespace-nowrap">{fmtDate(entry.timestamp)}</td>
                <td class="px-4 py-2.5 text-ink font-mono text-xs">{entry.actor}</td>
                <td class="px-4 py-2.5 text-ink font-mono text-xs">{entry.action}</td>
                <td class="px-4 py-2.5 text-ink-soft text-xs">{entry.resource ?? '—'}</td>
                <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{shortHash(entry.hash)}</td>
                <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{shortHash(entry.prev_hash)}</td>
                <td class="px-4 py-2.5">
                  {#if valid === null}
                    <span class="text-xs text-ink-mute" aria-label="Chain not verified">—</span>
                  {:else if valid}
                    <span class="text-success" title="Chain valid" aria-label="Chain valid">✓</span>
                  {:else}
                    <span class="text-danger font-bold" title="Chain broken" aria-label="Chain broken">✗</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</div>
