<script lang="ts">
  import { onMount } from 'svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import type { Conversation } from '$lib/api/types';
  import { loadConversations } from './loader';

  let items = $state<Conversation[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let disabled = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');
  let nextCursor = $state<string | undefined>(undefined);
  let prevCursors = $state<string[]>([]);
  let currentCursor = $state<string | undefined>(undefined);

  let filtered = $derived(
    filter
      ? items.filter((c) => JSON.stringify(c).toLowerCase().includes(filter.toLowerCase()))
      : items
  );

  async function load(cursor?: string) {
    loading = true;
    error = null;
    denied = false;
    disabled = false;
    const view = await loadConversations(cursor);
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
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Conversations</h1>
    <button onclick={() => load(currentCursor)} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <p class="text-sm text-ink-mute max-w-2xl">
    Durable bindings from a caller-owned conversation key to a provider chat thread. Reused keys resume the same
    chat so the end user keeps their context.
  </p>

  <!-- Filter -->
  <input
    type="search"
    bind:value={filter}
    placeholder="Filter by key, target, state…"
    class="w-full max-w-sm px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
  />

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
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
    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Conversation Key</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Target</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">State</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Last Used</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Last Job</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as conv (conv.tenant_id + '/' + conv.app_id + '/' + conv.target + '/' + conv.conversation_key)}
            <tr class="hover:bg-paper-soft transition-colors">
              <td class="px-4 py-2.5 font-mono text-xs text-ink break-all">{conv.conversation_key}</td>
              <td class="px-4 py-2.5 text-ink-soft">{conv.target}</td>
              <td class="px-4 py-2.5">
                {#if conv.state === 'broken'}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-danger-soft text-danger">broken</span>
                {:else}
                  <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success-soft text-success">active</span>
                {/if}
              </td>
              <td class="px-4 py-2.5 text-ink-mute text-xs">{fmtDate(conv.last_used_at)}</td>
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{conv.last_job_id ? conv.last_job_id.slice(0, 8) + '…' : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    <div class="flex items-center gap-3 text-sm">
      <button
        onclick={goPrev}
        disabled={prevCursors.length === 0}
        class="px-3 py-1.5 rounded-md border border-rule text-ink-soft hover:bg-paper-soft disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        ← Prev
      </button>
      <button
        onclick={goNext}
        disabled={!nextCursor}
        class="px-3 py-1.5 rounded-md border border-rule text-ink-soft hover:bg-paper-soft disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        Next →
      </button>
    </div>
  {/if}
</div>
