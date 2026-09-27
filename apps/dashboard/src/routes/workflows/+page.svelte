<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import WorkflowDag from '$lib/components/WorkflowDag.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import UpdatedAgo from '$lib/components/UpdatedAgo.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { pollWhileVisible } from '$lib/poll';
  import {
    assignStepIds,
    toCreateSteps,
    validateDraft,
    type DagDraftStep,
  } from '$lib/dag.js';
  import type { BrowserContext, Workflow, WorkflowRun } from '$lib/api/types';

  const API_VERSION = '2026-05-22';
  const PROVIDERS = [
    { key: 'chatgpt_web', label: 'ChatGPT' },
    { key: 'gemini_web', label: 'Gemini' },
    { key: 'deepseek_web', label: 'DeepSeek' },
    { key: 'duckai_web', label: 'Duck.ai' },
  ];

  let items = $state<Workflow[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let selectedWorkflow = $state<Workflow | null>(null);
  let contexts = $state<BrowserContext[]>([]);
  let createMode = $state<'ordered' | 'single'>('ordered');
  let createName = $state('ChatGPT Gemini DeepSeek workflow');
  let createTarget = $state('chatgpt_web');
  let createCommand = $state('submit');
  let createPrompt = $state('');
  let createLoading = $state(false);
  let createError = $state<string | null>(null);
  let createSuccess = $state<string | null>(null);
  let runLoading = $state(false);
  let runError = $state<string | null>(null);
  let runSuccess = $state<string | null>(null);

  // --- Custom DAG editor (modal) ---
  let dagOpen = $state(false);
  let dagName = $state('');
  let dagSteps = $state<DagDraftStep[]>([]);
  let dagKeySeq = $state(0);
  let dagLoading = $state(false);
  let dagError = $state<string | null>(null);

  function providerLabel(key: string): string {
    return PROVIDERS.find((p) => p.key === key)?.label ?? key;
  }

  function openDagEditor() {
    dagError = null;
    if (dagSteps.length === 0) {
      dagKeySeq += 1;
      dagSteps = [{
        key: `step-${dagKeySeq}`,
        target: createTarget,
        command: createCommand || 'submit',
        prompt: createPrompt,
        deps: [],
      }];
    }
    if (!dagName.trim()) dagName = createName.trim() || 'Custom workflow';
    dagOpen = true;
  }

  function closeDagEditor() {
    dagOpen = false;
  }

  function addDagStep() {
    dagKeySeq += 1;
    const prev = dagSteps[dagSteps.length - 1];
    dagSteps = [...dagSteps, {
      key: `step-${dagKeySeq}`,
      target: prev?.target ?? PROVIDERS[0].key,
      command: prev?.command || createCommand || 'submit',
      prompt: '',
      deps: [],
    }];
  }

  function removeDagStep(key: string) {
    dagSteps = dagSteps
      .filter((s) => s.key !== key)
      .map((s) => ({ ...s, deps: s.deps.filter((d) => d !== key) }));
  }

  function toggleDagDep(stepKey: string, depKey: string) {
    dagSteps = dagSteps.map((s) => {
      if (s.key !== stepKey) return s;
      const has = s.deps.includes(depKey);
      return { ...s, deps: has ? s.deps.filter((d) => d !== depKey) : [...s.deps, depKey] };
    });
  }

  let dagPreview = $derived.by(() => {
    const ids = assignStepIds(dagSteps);
    return {
      id: 'draft',
      name: dagName.trim() || 'Draft workflow',
      steps: dagSteps.map((s) => ({
        id: ids.get(s.key) ?? s.key,
        name: `${providerLabel(s.target)} — ${s.command.trim() || '…'}`,
        depends_on: s.deps.map((d) => ids.get(d) ?? d),
      })),
    };
  });

  async function createDagWorkflow() {
    dagError = null;
    const invalid = validateDraft(dagName, dagSteps);
    if (invalid) {
      dagError = invalid;
      return;
    }
    dagLoading = true;
    const res = await api.post<Workflow>('/v1/workflows', {
      api_version: API_VERSION,
      name: dagName.trim(),
      steps: toCreateSteps(dagSteps),
    });
    dagLoading = false;
    if (res.error) {
      dagError = res.error;
      return;
    }
    dagOpen = false;
    createSuccess = `Created ${res.data?.id?.slice(0, 8) ?? 'workflow'}`;
    await load(true);
  }

  let activeWorkflow = $derived(selectedWorkflow ?? items[0] ?? null);
  let activeStepCount = $derived(activeWorkflow?.step_count ?? activeWorkflow?.steps?.length ?? 0);
  let activeHasSteps = $derived(Boolean(activeWorkflow?.steps?.length));
  let providerState = $derived(
    Object.fromEntries(contexts.map((ctx) => [ctx.target_id, ctx.login_state ?? 'unknown']))
  );
  let orderedProviders = $derived(PROVIDERS.map((provider) => ({
    ...provider,
    loginState: providerState[provider.key] ?? 'unknown',
  })));
  let orderedHasUnknown = $derived(orderedProviders.some((provider) => provider.loginState !== 'authenticated'));

  let lastUpdated = $state<Date | null>(null);

  async function load(silent = false) {
    if (!silent) {
      loading = true;
      error = null;
      denied = false;
    }
    const res = await api.get('/v1/workflows');
    if (!silent) loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<Workflow>(res);
    // Keep the operator's selection across refreshes when it still exists.
    if (!selectedWorkflow || !items.some((w) => w.id === selectedWorkflow?.id)) selectedWorkflow = null;
    lastUpdated = new Date();
  }

  async function loadContexts() {
    const res = await api.get('/v1/browser/contexts');
    if (!res.error && !res.denied) contexts = listOf<BrowserContext>(res);
  }

  function workflowStepCount(workflow: Workflow): number {
    return workflow.step_count ?? workflow.steps?.length ?? 0;
  }

  async function createWorkflow() {
    createError = null;
    createSuccess = null;
    const prompt = createPrompt.trim();
    const name = createName.trim();
    const command = createCommand.trim();
    if (!name) {
      createError = 'Workflow name is required.';
      return;
    }
    if (!prompt) {
      createError = 'Prompt is required.';
      return;
    }
    if (!command) {
      createError = 'Command is required.';
      return;
    }
    createLoading = true;
    const steps = createMode === 'ordered'
      ? PROVIDERS.map((provider, index) => ({
          id: `step_${index + 1}_${provider.key}`,
          target: provider.key,
          command,
          input: { prompt },
        }))
      : [
          {
            id: 'step_1',
            target: createTarget,
            command,
            input: { prompt },
          },
        ];
    const res = await api.post<Workflow>('/v1/workflows', {
      api_version: API_VERSION,
      name,
      steps,
    });
    createLoading = false;
    if (res.error) {
      createError = res.error;
      return;
    }
    createSuccess = `Created ${res.data?.id?.slice(0, 8) ?? 'workflow'}`;
    createPrompt = '';
    await load(true);
  }

  async function runWorkflow(workflow: Workflow) {
    runError = null;
    runSuccess = null;
    runLoading = true;
    const res = await api.post<WorkflowRun>(`/v1/workflows/${workflow.id}/runs`, {
      api_version: API_VERSION,
    });
    runLoading = false;
    if (res.error) {
      runError = res.error;
      return;
    }
    runSuccess = `Run ${res.data?.id?.slice(0, 8) ?? ''} ${res.data?.state ?? 'queued'}`;
    await load(true);
  }

  onMount(() => {
    load();
    loadContexts();
    const stopPolling = pollWhileVisible(() => load(true), 45_000);
    return stopPolling;
  });
</script>

<div class="space-y-4">
  <PageHeader title="Workflows" subtitle="Ordered provider chains and custom step graphs, run on the gateway.">
    {#snippet actions()}
      <UpdatedAgo at={lastUpdated} />
      <button onclick={() => load()} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <form onsubmit={(e) => { e.preventDefault(); createWorkflow(); }} class="card space-y-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <h2 class="text-sm font-display font-semibold text-ink">Create Provider Workflow</h2>
      <div class="text-xs text-ink-mute font-mono">ChatGPT → Gemini → DeepSeek → Duck.ai</div>
    </div>

    <div class="grid gap-3 md:grid-cols-[1fr_1.4fr]">
      <div>
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Mode</span>
        <div class="grid grid-cols-2 rounded-md border border-rule overflow-hidden bg-paper" role="group" aria-label="Workflow mode">
          <button
            type="button"
            onclick={() => { createMode = 'ordered'; createName = 'ChatGPT Gemini DeepSeek workflow'; }}
            class="px-3 py-2 text-sm transition-colors"
            class:bg-accent-soft={createMode === 'ordered'}
            class:text-accent-deep={createMode === 'ordered'}
            class:font-medium={createMode === 'ordered'}
            class:text-ink-soft={createMode !== 'ordered'}
          >
            Ordered chain
          </button>
          <button
            type="button"
            onclick={() => { createMode = 'single'; createName = 'Provider prompt workflow'; }}
            class="px-3 py-2 text-sm transition-colors"
            class:bg-accent-soft={createMode === 'single'}
            class:text-accent-deep={createMode === 'single'}
            class:font-medium={createMode === 'single'}
            class:text-ink-soft={createMode !== 'single'}
          >
            Single provider
          </button>
        </div>
      </div>

      <div>
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Provider readiness</span>
        <div class="grid grid-cols-2 sm:grid-cols-4 rounded-md border border-rule overflow-hidden bg-paper">
          {#each orderedProviders as provider (provider.key)}
            <div class="min-w-0 px-3 py-2">
              <div class="truncate text-sm font-medium text-ink-soft">{provider.label}</div>
              <div class="truncate text-[11px] font-mono text-ink-mute mt-0.5">{provider.loginState}</div>
            </div>
          {/each}
        </div>
        {#if createMode === 'ordered' && orderedHasUnknown}
          <p class="mt-1.5 text-xs text-ink-mute">
            Ordered runs stop at the first provider that needs manual login.
          </p>
        {/if}
      </div>
    </div>

    <div class="grid gap-4 lg:grid-cols-[1fr_1.2fr_1fr]">
      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Name</span>
        <input bind:value={createName} class="input" />
      </label>

      {#if createMode === 'single'}
        <div class="sm:col-span-2 lg:col-span-1">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Provider</span>
          <div class="grid grid-cols-2 sm:grid-cols-4 rounded-md border border-rule overflow-hidden bg-paper" role="group" aria-label="Provider">
            {#each PROVIDERS as provider (provider.key)}
              <button
                type="button"
                onclick={() => { createTarget = provider.key; }}
                class="min-w-0 truncate px-3 py-2 text-sm transition-colors"
                class:bg-accent-soft={createTarget === provider.key}
                class:text-accent-deep={createTarget === provider.key}
                class:font-medium={createTarget === provider.key}
                class:text-ink-soft={createTarget !== provider.key}
              >
                {provider.label}
              </button>
            {/each}
          </div>
        </div>
      {:else}
        <div class="sm:col-span-2 lg:col-span-1">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Steps</span>
          <div class="rounded-md border border-rule bg-paper px-3 py-2 text-sm text-ink-soft">
            1. ChatGPT <span class="text-ink-mute">→</span> 2. Gemini <span class="text-ink-mute">→</span> 3. DeepSeek <span class="text-ink-mute">→</span> 4. Duck.ai
          </div>
        </div>
      {/if}

      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Command</span>
        <input bind:value={createCommand} class="input" />
      </label>
    </div>

    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Prompt</span>
      <textarea
        bind:value={createPrompt}
        rows="3"
        placeholder="Enter the workflow prompt..."
        class="input resize-y"
      ></textarea>
    </label>

    <div class="flex items-center gap-3 flex-wrap">
      <button type="submit" disabled={createLoading} class="btn btn-primary">
        {createLoading ? 'Creating...' : 'Create Workflow'}
      </button>
      <button type="button" onclick={() => openDagEditor()} class="btn btn-secondary">
        Custom DAG…
      </button>
      {#if createError}
        <span class="text-xs text-danger">{createError}</span>
      {/if}
      {#if createSuccess}
        <span class="text-xs text-success">{createSuccess}</span>
      {/if}
    </div>
  </form>

  {#if loading}
    <SkeletonTable rows={4} cols={3} />
  {:else if denied}
    <DeniedPanel resource="workflows" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    {#if items.length === 0}
      <EmptyState message="No workflows found." hint="Create a workflow through the gateway API to display its metadata here." />
    {:else}
      <div class="flex flex-col gap-4 min-h-0 lg:flex-row">
        <div class="w-full lg:w-64 lg:shrink-0">
          <div class="rounded-md border border-rule overflow-hidden">
            <div class="px-4 py-2 bg-paper-soft border-b border-rule text-xs font-medium text-ink-mute uppercase tracking-wider">
              Workflows
            </div>
            <ul class="divide-y divide-rule max-h-80 lg:max-h-none overflow-y-auto" role="list">
              {#each items as wf (wf.id)}
                <li>
                  <button
                    onclick={() => { selectedWorkflow = wf; }}
                    class="w-full min-h-16 text-left px-4 py-3 text-sm transition-colors hover:bg-paper-soft"
                    class:bg-accent-soft={activeWorkflow?.id === wf.id}
                    class:text-accent-deep={activeWorkflow?.id === wf.id}
                    class:font-medium={activeWorkflow?.id === wf.id}
                    class:text-ink-soft={activeWorkflow?.id !== wf.id}
                    aria-pressed={activeWorkflow?.id === wf.id}
                  >
                    <div class="font-medium truncate">{wf.name}</div>
                    <div class="text-xs font-mono text-ink-mute mt-0.5">{wf.id.slice(0, 8)}...</div>
                    <div class="text-xs text-ink-mute mt-0.5">
                      {workflowStepCount(wf)} step{workflowStepCount(wf) !== 1 ? 's' : ''}
                    </div>
                    {#if wf.status}
                      <div class="text-xs text-ink-mute mt-0.5">{wf.status}</div>
                    {/if}
                  </button>
                </li>
              {/each}
            </ul>
          </div>
        </div>

        <div class="flex-1 min-w-0">
          {#if activeWorkflow}
            <div class="space-y-3">
              <div class="flex flex-wrap items-center gap-3">
                <h2 class="text-lg font-display font-semibold text-ink min-w-0 truncate">{activeWorkflow.name}</h2>
                {#if activeWorkflow.status}
                  <span class="text-xs px-2 py-0.5 rounded-pill bg-paper-soft border border-rule text-ink-soft font-mono">{activeWorkflow.status}</span>
                {/if}
                <button
                  onclick={() => runWorkflow(activeWorkflow!)}
                  disabled={runLoading}
                  class="btn btn-sm ml-auto border border-accent-deep/40 text-accent-deep hover:bg-accent-soft"
                >
                  {runLoading ? 'Running...' : 'Run'}
                </button>
              </div>

              {#if runError}
                <div class="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-xs text-danger">{runError}</div>
              {/if}
              {#if runSuccess}
                <div class="rounded-md border border-success/30 bg-success-soft px-3 py-2 text-xs text-success">{runSuccess}</div>
              {/if}

              <p class="text-xs text-ink-mute">
                {activeStepCount} step{activeStepCount !== 1 ? 's' : ''}
              </p>

              {#if activeHasSteps}
                <WorkflowDag workflow={activeWorkflow} />

                <div class="flex items-center gap-4 text-xs text-ink-mute flex-wrap">
                  <span class="font-medium">Status:</span>
                  <span class="flex items-center gap-1.5">
                    <span class="w-3 h-3 rounded-sm inline-block bg-success"></span> completed
                  </span>
                  <span class="flex items-center gap-1.5">
                    <span class="w-3 h-3 rounded-sm inline-block bg-marine"></span> running
                  </span>
                  <span class="flex items-center gap-1.5">
                    <span class="w-3 h-3 rounded-sm inline-block bg-warning"></span> pending
                  </span>
                  <span class="flex items-center gap-1.5">
                    <span class="w-3 h-3 rounded-sm inline-block bg-danger"></span> failed
                  </span>
                </div>
              {:else}
                <EmptyState
                  message="Workflow graph unavailable."
                  hint="The current list endpoint exposes workflow metadata only; step details will render here when the gateway returns them."
                />
              {/if}
            </div>
          {:else}
            <EmptyState message="Select a workflow to view its details." />
          {/if}
        </div>
      </div>
    {/if}
  {/if}
</div>

<!-- Custom DAG editor dialog -->
<Modal bind:open={dagOpen} title="Custom DAG" width="lg" onClose={closeDagEditor}>
  <div class="space-y-4">
    <p class="text-xs text-ink-mute">Compose arbitrary step graphs. Steps with no dependencies follow the previous step; the gateway rejects cycles and dangling references.</p>
    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Workflow name *</span>
      <input
        type="text"
        bind:value={dagName}
        placeholder="e.g. research-fanout"
        class="input"
      />
    </label>

    {#each dagSteps as step, i (step.key)}
      <div class="card space-y-3">
        <div class="flex items-center justify-between">
          <p class="text-sm font-mono font-semibold text-accent-deep">step_{i + 1}</p>
          <button
            onclick={() => removeDagStep(step.key)}
            disabled={dagSteps.length <= 1}
            class="btn-link text-xs text-danger disabled:text-danger"
          >
            Remove
          </button>
        </div>
        <div class="grid gap-3 md:grid-cols-2">
          <label class="block">
            <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Target *</span>
            <select bind:value={step.target} class="input">
              {#each PROVIDERS as provider (provider.key)}
                <option value={provider.key}>{provider.label}</option>
              {/each}
            </select>
          </label>
          <label class="block">
            <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Command *</span>
            <input type="text" bind:value={step.command} placeholder="submit" class="input" />
          </label>
        </div>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Prompt</span>
          <textarea
            bind:value={step.prompt}
            rows="2"
            placeholder="Step prompt (optional)"
            class="input resize-y"
          ></textarea>
        </label>
        {#if dagSteps.length > 1}
          <div>
            <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Depends on (empty = previous step)</span>
            <div class="flex gap-3 flex-wrap">
              {#each dagSteps as other (other.key)}
                {#if other.key !== step.key}
                  {@const depId = `step_${dagSteps.findIndex((s) => s.key === other.key) + 1}`}
                  <label class="flex min-h-6 items-center gap-1.5 text-xs text-ink-soft cursor-pointer">
                    <input
                      type="checkbox"
                      checked={step.deps.includes(other.key)}
                      onchange={() => toggleDagDep(step.key, other.key)}
                      class="accent-[var(--color-accent)]"
                    />
                    <span class="font-mono">{depId}</span>
                  </label>
                {/if}
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/each}

    <button onclick={() => addDagStep()} class="btn btn-secondary btn-sm">
      + Add step
    </button>

    <div>
      <p class="text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Live preview</p>
      <WorkflowDag workflow={dagPreview} />
    </div>

    {#if dagError}
      <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{dagError}</div>
    {/if}
  </div>
  {#snippet footer()}
    <button onclick={() => closeDagEditor()} class="btn btn-secondary">Cancel</button>
    <button onclick={() => createDagWorkflow()} disabled={dagLoading} class="btn btn-primary">
      {dagLoading ? 'Creating…' : 'Create Workflow'}
    </button>
  {/snippet}
</Modal>
