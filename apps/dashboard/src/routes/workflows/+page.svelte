<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import WorkflowDag from '$lib/components/WorkflowDag.svelte';
  import type { BrowserContext, Workflow, WorkflowRun } from '$lib/api/types';

  const API_VERSION = '2026-05-22';
  const PROVIDERS = [
    { key: 'chatgpt_web', label: 'ChatGPT' },
    { key: 'gemini_web', label: 'Gemini' },
    { key: 'deepseek_web', label: 'DeepSeek' },
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

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/workflows');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<Workflow>(res);
    selectedWorkflow = null;
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
    await load();
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
    await load();
  }

  onMount(() => {
    load();
    loadContexts();
  });
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Workflows</h1>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <form onsubmit={(e) => { e.preventDefault(); createWorkflow(); }} class="rounded-md border border-rule bg-paper-soft p-4 space-y-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <h2 class="text-sm font-display font-semibold text-ink">Create Provider Workflow</h2>
      <div class="text-xs text-ink-mute font-mono">ChatGPT -> Gemini -> DeepSeek</div>
    </div>

    <div class="grid gap-3 md:grid-cols-[1fr_1.4fr]">
      <div>
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Mode</span>
        <div class="grid grid-cols-2 rounded-md border border-rule overflow-hidden bg-paper" role="group" aria-label="Workflow mode">
          <button
            type="button"
            onclick={() => { createMode = 'ordered'; createName = 'ChatGPT Gemini DeepSeek workflow'; }}
            class="px-3 py-2 text-sm border-r border-rule transition-colors"
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
        <div class="grid grid-cols-3 rounded-md border border-rule overflow-hidden bg-paper">
          {#each orderedProviders as provider}
            <div class="px-3 py-2 border-r border-rule last:border-r-0">
              <div class="text-sm font-medium text-ink-soft">{provider.label}</div>
              <div class="text-[11px] font-mono text-ink-mute mt-0.5">{provider.loginState}</div>
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
        <input
          bind:value={createName}
          class="w-full px-3 py-2 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
        />
      </label>

      {#if createMode === 'single'}
        <div>
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Provider</span>
          <div class="grid grid-cols-3 rounded-md border border-rule overflow-hidden bg-paper" role="group" aria-label="Provider">
            {#each PROVIDERS as provider}
              <button
                type="button"
                onclick={() => { createTarget = provider.key; }}
                class="px-3 py-2 text-sm border-r border-rule last:border-r-0 transition-colors"
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
        <div>
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Steps</span>
          <div class="rounded-md border border-rule bg-paper px-3 py-2 text-sm text-ink-soft">
            1. ChatGPT <span class="text-ink-mute">-></span> 2. Gemini <span class="text-ink-mute">-></span> 3. DeepSeek
          </div>
        </div>
      {/if}

      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Command</span>
        <input
          bind:value={createCommand}
          class="w-full px-3 py-2 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
        />
      </label>
    </div>

    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Prompt</span>
      <textarea
        bind:value={createPrompt}
        rows="3"
        placeholder="Enter the workflow prompt..."
        class="w-full px-3 py-2 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40 resize-y"
      ></textarea>
    </label>

    <div class="flex items-center gap-3 flex-wrap">
      <button
        type="submit"
        disabled={createLoading}
        class="px-4 py-2 rounded-md bg-accent text-paper text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {createLoading ? 'Creating...' : 'Create Workflow'}
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
    <div class="text-ink-mute text-sm">Loading...</div>
  {:else if denied}
    <DeniedPanel resource="workflows" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    {#if items.length === 0}
      <EmptyState message="No workflows found." hint="Create a workflow through the gateway API to display its metadata here." />
    {:else}
      <div class="flex gap-4 min-h-0">
        <div class="w-64 shrink-0">
          <div class="rounded-md border border-rule overflow-hidden">
            <div class="px-4 py-2 bg-paper-soft border-b border-rule text-xs font-medium text-ink-mute uppercase tracking-wider">
              Workflows
            </div>
            <ul class="divide-y divide-rule" role="list">
              {#each items as wf (wf.id)}
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
              <div class="flex items-center gap-3">
                <h2 class="text-lg font-display font-semibold text-ink">{activeWorkflow.name}</h2>
                {#if activeWorkflow.status}
                  <span class="text-xs px-2 py-0.5 rounded-full bg-paper-soft border border-rule text-ink-soft font-mono">{activeWorkflow.status}</span>
                {/if}
                <button
                  onclick={() => runWorkflow(activeWorkflow!)}
                  disabled={runLoading}
                  class="ml-auto px-3 py-1.5 rounded-md border border-accent-deep/40 text-accent-deep text-xs font-medium hover:bg-accent-soft disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
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
