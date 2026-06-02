<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import WorkflowDag from '$lib/components/WorkflowDag.svelte';
  import type { Workflow, ListResponse } from '$lib/api/types';

  const fixtureWorkflow: Workflow = {
    id: 'wf-fixture',
    name: 'Example Pipeline',
    steps: [
      { id: 's1', name: 'Fetch', status: 'completed' },
      { id: 's2', name: 'Validate', status: 'running', depends_on: ['s1'] },
      { id: 's3', name: 'Transform', status: 'pending', depends_on: ['s1'] },
      { id: 's4', name: 'Load', status: 'pending', depends_on: ['s2', 's3'] },
    ],
  };

  let items = $state<Workflow[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let selectedWorkflow = $state<Workflow | null>(null);

  // When API returns empty, show fixture; otherwise show first workflow by default
  let displayItems = $derived(items.length > 0 ? items : [fixtureWorkflow]);
  let activeWorkflow = $derived(selectedWorkflow ?? displayItems[0] ?? null);

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get<ListResponse<Workflow>>('/v1/workflows');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = res.data?.items ?? [];
    selectedWorkflow = null;
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Workflows</h1>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="workflows" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    <div class="flex gap-4 min-h-0">
      <!-- Left panel: workflow list -->
      <div class="w-64 shrink-0">
        <div class="rounded-md border border-rule overflow-hidden">
          <div class="px-4 py-2 bg-paper-soft border-b border-rule text-xs font-medium text-ink-mute uppercase tracking-wider">
            {items.length === 0 ? 'Fixture (no API data)' : 'Workflows'}
          </div>
          <ul class="divide-y divide-rule" role="list">
            {#each displayItems as wf (wf.id)}
              <li>
                <button
                  onclick={() => { selectedWorkflow = wf; }}
                  class="w-full text-left px-4 py-3 text-sm transition-colors hover:bg-paper-soft"
                  class:bg-accent-soft={activeWorkflow?.id === wf.id}
                  class:text-accent-deep={activeWorkflow?.id === wf.id}
                  class:font-medium={activeWorkflow?.id === wf.id}
                  class:text-ink-soft={activeWorkflow?.id !== wf.id}
                  aria-pressed={activeWorkflow?.id === wf.id}
                >
                  <div class="font-medium truncate">{wf.name}</div>
                  <div class="text-xs font-mono text-ink-mute mt-0.5">{wf.id.slice(0, 8)}…</div>
                  {#if wf.status}
                    <div class="text-xs text-ink-mute mt-0.5">{wf.status}</div>
                  {/if}
                </button>
              </li>
            {/each}
          </ul>
        </div>
      </div>

      <!-- Right panel: DAG viewer -->
      <div class="flex-1 min-w-0">
        {#if activeWorkflow}
          <div class="space-y-3">
            <div class="flex items-center gap-3">
              <h2 class="text-lg font-display font-semibold text-ink">{activeWorkflow.name}</h2>
              {#if activeWorkflow.id === 'wf-fixture'}
                <span class="text-xs px-2 py-0.5 rounded-full bg-paper-warm border border-rule text-ink-mute font-mono">fixture</span>
              {/if}
              {#if activeWorkflow.status}
                <span class="text-xs px-2 py-0.5 rounded-full bg-paper-soft border border-rule text-ink-soft font-mono">{activeWorkflow.status}</span>
              {/if}
            </div>

            <!-- Step count info -->
            <p class="text-xs text-ink-mute">
              {activeWorkflow.steps?.length ?? 0} step{activeWorkflow.steps?.length !== 1 ? 's' : ''}
            </p>

            <!-- DAG visualization -->
            <WorkflowDag workflow={activeWorkflow} />

            <!-- Legend -->
            <div class="flex items-center gap-4 text-xs text-ink-mute flex-wrap">
              <span class="font-medium">Status:</span>
              <span class="flex items-center gap-1.5">
                <span class="w-3 h-3 rounded-sm inline-block" style="background:#50a082"></span> completed
              </span>
              <span class="flex items-center gap-1.5">
                <span class="w-3 h-3 rounded-sm inline-block" style="background:#366290"></span> running
              </span>
              <span class="flex items-center gap-1.5">
                <span class="w-3 h-3 rounded-sm inline-block" style="background:#b08840"></span> pending
              </span>
              <span class="flex items-center gap-1.5">
                <span class="w-3 h-3 rounded-sm inline-block" style="background:#b04040"></span> failed
              </span>
            </div>
          </div>
        {:else}
          <EmptyState message="Select a workflow to view its DAG." />
        {/if}
      </div>
    </div>
  {/if}
</div>
