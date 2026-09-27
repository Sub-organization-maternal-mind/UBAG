<script lang="ts">
  import { onMount } from 'svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import type { Conversation } from '$lib/api/types';
  import { loadConversations } from './loader';

  let items = $state<Conversation[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let disabled = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');
  let filterQuery = $state('');
  let filterTimer: ReturnType<typeof setTimeout> | undefined;
  let nextCursor = $state<string | undefined>(undefined);
  let prevCursors = $state<string[]>([]);
  let currentCursor = $state<string | undefined>(undefined);

  function onFilterInput(e: Event) {
    const value = (e.currentTarget as HTMLInputElement).value;
    filter = value;
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => (filterQuery = value), 120);
  }

  let filtered = $derived.by(() => {
    const q = filterQuery.trim().toLowerCase();
    if (!q) return items;
    return items.filter((c) =>
      `${c.conversation_key ?? ''} ${c.target ?? ''} ${c.state ?? ''}`
        .toLowerCase()
        .includes(q)
    );
  });

  let loadSeq = 0;
  async function load(cursor?: string, silent = false) {
    const seq = ++loadSeq;
    if (!silent) {
      loading = true;
      error = null;
      denied = false;
      disabled = false;
    }
    const view = await loadConversations(cursor);
    if (seq !== loadSeq) return; // a newer load superseded this one
    loading = false;
    if (view.kind === 'denied') { denied = true; return; }
    if (view.kind === 'disabled') { disabled = true; return; }
    if (view.kind === 'error') { error = view.message; return; }
    items = view.conversations;
    nextCursor = view.nextCursor ?? undefined;
  }

  function goNext() {
    if (!nextCursor) return;
    prevCursors = [...prevCursors, currentCursor as string];
    currentCursor = nextCursor;
    load(nextCursor);
  }

  function goPrev() {
    const prev = prevCursors[prevCursors.length - 1];
    prevCursors = prevCursors.slice(0, -1);
    currentCursor = prev;
    load(prev);
  }

  function fmtDate(s: string): string {
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <PageHeader title="Conversations" subtitle="Durable bindings from a caller-owned conversation key to a provider chat thread. Reused keys resume the same chat so the end user keeps their context.">
    {#snippet actions()}
      <button onclick={() => load(currentCursor)} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <!-- Filter -->
  <input
    type="search"
    value={filter}
    oninput={onFilterInput}
    placeholder="Filter by key, target, state…"
    class="input max-w-sm"
  />

  {#if loading}
    <SkeletonTable rows={6} cols={5} />
  {:else if denied}
    <DeniedPanel resource="conversations" />
  {:else if disabled}
    <EmptyState
      message="Conversations are not enabled on this gateway."
      hint="The gateway returned 501 for /v1/conversations. An operator can enable conversation affinity by setting UBAG_CONVERSATIONS_ENABLED=true."
    />
  {:else if error}
    <ErrorPanel message={error} retry={() => load(currentCursor)} />
  {:else if filtered.length === 0}
    <EmptyState message="No conversations found." hint={filter ? 'Try clearing the filter.' : ''} />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">Conversation Key</th>
            <th class="th">Target</th>
            <th class="th">State</th>
            <th class="th">Last Used</th>
            <th class="th">Last Job</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as conv (conv.tenant_id + '/' + conv.app_id + '/' + conv.target + '/' + conv.conversation_key)}
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink break-all max-w-[16rem]">{conv.conversation_key}</td>
              <td class="td">{conv.target}</td>
              <td class="td">
                {#if conv.state === 'broken'}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-danger-soft text-danger">broken</span>
                {:else}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success-soft text-success">active</span>
                {/if}
              </td>
              <td class="td text-xs text-ink-mute whitespace-nowrap">{fmtDate(conv.last_used_at)}</td>
              <td class="td font-mono text-xs text-ink-mute">{conv.last_job_id ? conv.last_job_id.slice(0, 8) + '…' : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    <div class="flex items-center gap-3 text-sm">
      <button onclick={goPrev} disabled={prevCursors.length === 0} class="btn btn-secondary btn-sm">
        ← Prev
      </button>
      <button onclick={goNext} disabled={!nextCursor} class="btn btn-secondary btn-sm">
        Next →
      </button>
    </div>
  {/if}
</div>
