<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import type { AuditEntry } from '$lib/api/types';

  const PAGE_SIZE = 50;

  let items = $state<AuditEntry[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filterActor = $state('');
  let filterAction = $state('');
  let page = $state(0);
  let exporting = $state(false);
  let exportError = $state<string | null>(null);
  let exportSummary = $state<string | null>(null);

  let filtered = $derived.by(() => {
    let result = items;
    if (filterActor) {
      result = result.filter(e => e.actor.toLowerCase().includes(filterActor.toLowerCase()));
    }
    if (filterAction) {
      result = result.filter(e => e.action.toLowerCase().includes(filterAction.toLowerCase()));
    }
    return result;
  });

  // Chain verification (items[i].prev_hash === items[i-1].hash) computed in a
  // single pass per filtered dataset — not per row, per render.
  let chainResults = $derived.by(() => {
    const results: Array<boolean | null> = [];
    for (let i = 0; i < filtered.length; i++) {
      if (i === 0) { results.push(null); continue; }
      const prev = filtered[i - 1];
      const cur = filtered[i];
      if (!cur.prev_hash || !prev.hash) { results.push(null); continue; }
      results.push(cur.prev_hash === prev.hash);
    }
    return results;
  });

  let pageCount = $derived(Math.max(1, Math.ceil(filtered.length / PAGE_SIZE)));
  // Clamp the page when filters shrink the result set.
  let safePage = $derived(Math.min(page, pageCount - 1));
  let pageItems = $derived(filtered.slice(safePage * PAGE_SIZE, safePage * PAGE_SIZE + PAGE_SIZE));

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

  async function exportChain() {
    exporting = true;
    exportError = null;
    exportSummary = null;
    const res = await api.post<Record<string, unknown>>('/v1/audit/export', {});
    exporting = false;
    if (res.denied) { exportError = 'Export denied for this role.'; return; }
    if (res.error || !res.data) { exportError = res.error ?? 'Export failed.'; return; }
    const body = res.data as {
      chain_valid?: boolean; head_hash?: string; count?: number; records?: unknown[];
    };
    exportSummary = `Exported ${body.count ?? body.records?.length ?? 0} records — chain ${body.chain_valid ? 'valid' : 'INVALID'} (head ${String(body.head_hash ?? '?').slice(-12)}).`;
    const blob = new Blob([JSON.stringify(res.data, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `ubag-audit-export-${new Date().toISOString().slice(0, 10)}.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 5000);
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <PageHeader title="Audit Log" subtitle="Append-only audit chain with hash-link integrity verification.">
    {#snippet actions()}
      <button onclick={() => exportChain()} disabled={exporting || loading} class="btn btn-secondary btn-sm">
        {exporting ? 'Exporting…' : 'Export chain'}
      </button>
      <button onclick={() => load()} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  {#if exportError}
    <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{exportError}</div>
  {/if}
  {#if exportSummary}
    <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">{exportSummary}</div>
  {/if}

  <!-- Filters -->
  <div class="flex gap-3 flex-wrap">
    <input
      type="search"
      bind:value={filterActor}
      placeholder="Filter by actor…"
      class="input max-w-xs"
    />
    <input
      type="search"
      bind:value={filterAction}
      placeholder="Filter by action…"
      class="input max-w-xs"
    />
  </div>

  {#if loading}
    <SkeletonTable rows={10} cols={7} />
  {:else if denied}
    <DeniedPanel resource="audit log" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    {#if filtered.length === 0}
      <EmptyState message="No audit entries found." hint={filterActor || filterAction ? 'Try clearing the filters.' : ''} />
    {:else}
      <!-- Chain integrity summary -->
      {@const chainIssues = chainResults.filter((v) => v === false).length}
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

      <div class="table-wrap">
        <table class="w-full text-sm">
          <thead class="thead">
            <tr>
              <th class="th">Timestamp</th>
              <th class="th">Actor</th>
              <th class="th">Action</th>
              <th class="th">Resource</th>
              <th class="th">Hash</th>
              <th class="th">Prev Hash</th>
              <th class="th">Chain</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each pageItems as entry, i (entry.id)}
              {@const valid = chainResults[safePage * PAGE_SIZE + i]}
              <tr class="transition-colors hover:bg-paper-soft/70" class:bg-danger-soft={valid === false}>
                <td class="td text-xs text-ink-mute whitespace-nowrap">{fmtDate(entry.timestamp)}</td>
                <td class="td font-mono text-xs text-ink">{entry.actor}</td>
                <td class="td font-mono text-xs text-ink">{entry.action}</td>
                <td class="td text-xs max-w-[16rem] truncate" title={entry.resource}>{entry.resource ?? '—'}</td>
                <td class="td font-mono text-xs text-ink-mute">{shortHash(entry.hash)}</td>
                <td class="td font-mono text-xs text-ink-mute">{shortHash(entry.prev_hash)}</td>
                <td class="td">
                  {#if valid === null}
                    <span class="text-xs text-ink-mute" aria-label="Chain not verified">—</span>
                  {:else if valid}
                    <span class="text-success" title="Chain valid" aria-label="Chain valid">✓</span>
                  {:else}
                    <span class="font-bold text-danger" title="Chain broken" aria-label="Chain broken">✗</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <!-- Pagination -->
      {#if pageCount > 1}
        <div class="flex items-center justify-between gap-3 text-sm">
          <button onclick={() => (page = Math.max(0, safePage - 1))} disabled={safePage === 0} class="btn btn-secondary btn-sm">
            ← Prev
          </button>
          <span class="text-xs text-ink-mute font-mono">
            page {safePage + 1} of {pageCount} · {filtered.length} entries
          </span>
          <button onclick={() => (page = Math.min(pageCount - 1, safePage + 1))} disabled={safePage >= pageCount - 1} class="btn btn-secondary btn-sm">
            Next →
          </button>
        </div>
      {/if}
    {/if}
  {/if}
</div>
