<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import type { BrowserContext, Job, JobCreateResponse, JobEnvelope, JobsResponse, Template } from '$lib/api/types';

  const API_VERSION = '2026-05-22';
  const PROVIDERS = [
    { key: 'chatgpt_web', label: 'ChatGPT' },
    { key: 'gemini_web', label: 'Gemini' },
    { key: 'deepseek_web', label: 'DeepSeek' },
  ];

  let items = $state<Job[]>([]);
  let templates = $state<Template[]>([]);
  let contexts = $state<BrowserContext[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');
  let nextCursor = $state<string | undefined>(undefined);
  let prevCursors = $state<string[]>([]);
  let currentCursor = $state<string | undefined>(undefined);

  // Detail drawer
  let selectedJob = $state<Job | null>(null);
  let drawerOpen = $state(false);
  let cancelLoading = $state(false);
  let retryLoading = $state(false);
  let actionError = $state<string | null>(null);
  let actionSuccess = $state<string | null>(null);

  let dialogEl = $state<HTMLDialogElement | null>(null);
  let createTarget = $state('chatgpt_web');
  let createCommandType = $state('submit');
  let createTemplateId = $state('');
  let createPrompt = $state('');
  let createLoading = $state(false);
  let createError = $state<string | null>(null);
  let createSuccess = $state<string | null>(null);

  let filtered = $derived(
    filter
      ? items.filter((j) => JSON.stringify(j).toLowerCase().includes(filter.toLowerCase()))
      : items
  );
  let providerState = $derived(
    Object.fromEntries(contexts.map((ctx) => [ctx.target_id, ctx.login_state ?? 'unknown']))
  );
  let matchingTemplates = $derived(
    templates.filter((template) => !createCommandType || template.command_type === createCommandType)
  );

  async function load(cursor?: string) {
    loading = true;
    error = null;
    denied = false;
    const path = cursor ? `/v1/jobs?cursor=${encodeURIComponent(cursor)}&limit=20` : '/v1/jobs?limit=20';
    const res = await api.get<JobsResponse>(path);
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = res.data?.jobs ?? [];
    nextCursor = res.data?.next_cursor;
  }

  async function loadSupportData() {
    const [templateRes, contextRes] = await Promise.all([
      api.get('/v1/templates'),
      api.get('/v1/browser/contexts'),
    ]);
    if (!templateRes.error && !templateRes.denied) templates = listOf<Template>(templateRes);
    if (!contextRes.error && !contextRes.denied) contexts = listOf<BrowserContext>(contextRes);
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

  function openDrawer(job: Job) {
    selectedJob = job;
    actionError = null;
    actionSuccess = null;
    drawerOpen = true;
    // Use requestAnimationFrame to ensure dialog is in DOM
    requestAnimationFrame(() => {
      dialogEl?.showModal();
    });
  }

  function closeDrawer() {
    drawerOpen = false;
    dialogEl?.close();
    selectedJob = null;
  }

  async function cancelJob() {
    if (!selectedJob) return;
    cancelLoading = true;
    actionError = null;
    actionSuccess = null;
    const res = await api.post(`/v1/jobs/${selectedJob.id}/cancel`, {
      api_version: API_VERSION,
      reason: 'dashboard operator',
    });
    cancelLoading = false;
    if (res.error) { actionError = res.error; return; }
    actionSuccess = 'Job cancelled.';
    await load(currentCursor);
    if (selectedJob) {
      const updated = items.find((j) => j.id === selectedJob!.id);
      if (updated) selectedJob = updated;
    }
  }

  async function retryJob() {
    if (!selectedJob) return;
    retryLoading = true;
    actionError = null;
    actionSuccess = null;
    const res = await api.post(`/v1/jobs/${selectedJob.id}/retry`, {
      api_version: API_VERSION,
    });
    retryLoading = false;
    if (res.error) { actionError = res.error; return; }
    actionSuccess = 'Retry queued.';
    await load(currentCursor);
  }

  function buildJobEnvelope(): JobEnvelope {
    const job: JobEnvelope['job'] = {
      target: createTarget,
      command_type: createCommandType.trim(),
      input: { prompt: createPrompt.trim() },
      options: { priority: 'normal', return_mode: 'final' },
    };
    if (createTemplateId) job.template_id = createTemplateId;
    return {
      api_version: API_VERSION,
      client: {
        app_id: 'ubag-dashboard',
        app_version: '0.0.0',
        sdk: { name: 'ubag-dashboard', version: '0.0.0' },
      },
      job,
    };
  }

  async function createJob() {
    createError = null;
    createSuccess = null;
    const prompt = createPrompt.trim();
    if (!prompt) {
      createError = 'Prompt is required.';
      return;
    }
    if (!createCommandType.trim()) {
      createError = 'Command type is required.';
      return;
    }
    createLoading = true;
    const res = await api.post<JobCreateResponse>('/v1/jobs', buildJobEnvelope());
    createLoading = false;
    if (res.error) {
      createError = res.error;
      return;
    }
    createSuccess = `Created job ${res.data?.job_id?.slice(0, 8) ?? ''}`;
    createPrompt = '';
    await load(currentCursor);
  }

  function fmtDate(s: string): string {
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  onMount(() => {
    load();
    loadSupportData();
  });
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Jobs</h1>
    <button onclick={() => load(currentCursor)} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <form onsubmit={(e) => { e.preventDefault(); createJob(); }} class="rounded-md border border-rule bg-paper-soft p-4 space-y-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <h2 class="text-sm font-display font-semibold text-ink">Submit Provider Job</h2>
      <div class="text-xs text-ink-mute font-mono">ChatGPT -> Gemini -> DeepSeek</div>
    </div>

    <div class="grid gap-4 lg:grid-cols-[1.2fr_1fr_1fr]">
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
              <span>{provider.label}</span>
              <span class="block text-[11px] font-mono text-ink-mute mt-0.5">{providerState[provider.key] ?? 'unknown'}</span>
            </button>
          {/each}
        </div>
      </div>

      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Command Type</span>
        <input
          bind:value={createCommandType}
          class="w-full px-3 py-2 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
        />
      </label>

      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Template</span>
        <select
          bind:value={createTemplateId}
          class="w-full px-3 py-2 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
        >
          <option value="">No template</option>
          {#each matchingTemplates as template (template.id)}
            <option value={template.id}>{template.id}</option>
          {/each}
        </select>
      </label>
    </div>

    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Prompt</span>
      <textarea
        bind:value={createPrompt}
        rows="4"
        placeholder="Enter the provider prompt..."
        class="w-full px-3 py-2 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40 resize-y"
      ></textarea>
    </label>

    <div class="flex items-center gap-3 flex-wrap">
      <button
        type="submit"
        disabled={createLoading}
        class="px-4 py-2 rounded-md bg-accent text-paper text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {createLoading ? 'Submitting...' : 'Submit Job'}
      </button>
      {#if createError}
        <span class="text-xs text-danger">{createError}</span>
      {/if}
      {#if createSuccess}
        <span class="text-xs text-success">{createSuccess}</span>
      {/if}
    </div>
  </form>

  <!-- Filter -->
  <input
    type="search"
    bind:value={filter}
    placeholder="Filter by status, target, type…"
    class="w-full max-w-sm px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
  />

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="jobs" />
  {:else if error}
    <ErrorPanel message={error} retry={() => load(currentCursor)} />
  {:else if filtered.length === 0}
    <EmptyState message="No jobs found." hint={filter ? 'Try clearing the filter.' : ''} />
  {:else}
    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Target</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Command Type</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Created At</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as job (job.id)}
            <tr
              class="hover:bg-paper-soft transition-colors cursor-pointer"
              onclick={() => openDrawer(job)}
              tabindex="0"
              role="button"
              aria-label="View job {job.id}"
              onkeydown={(e) => e.key === 'Enter' && openDrawer(job)}
            >
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{job.id.slice(0, 8)}…</td>
              <td class="px-4 py-2.5 text-ink-soft">{job.target}</td>
              <td class="px-4 py-2.5 text-ink-soft font-mono text-xs">{job.command_type}</td>
              <td class="px-4 py-2.5"><StatusBadge status={job.status} /></td>
              <td class="px-4 py-2.5 text-ink-mute text-xs">{fmtDate(job.created_at)}</td>
              <td class="px-4 py-2.5">
                <button
                  onclick={(e) => { e.stopPropagation(); openDrawer(job); }}
                  class="text-xs text-accent-deep hover:underline"
                >
                  Details
                </button>
              </td>
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

<!-- Job detail drawer (dialog) -->
{#if drawerOpen}
  <dialog
    bind:this={dialogEl}
    class="fixed inset-y-0 right-0 m-0 h-full w-full max-w-lg bg-paper border-l border-rule shadow-2xl overflow-y-auto p-0"
    aria-label="Job details"
    onclose={closeDrawer}
  >
    <div class="sticky top-0 z-10 flex items-center justify-between px-5 py-4 border-b border-rule bg-paper-soft">
      <h2 class="text-lg font-display font-semibold text-ink">Job Details</h2>
      <button
        onclick={closeDrawer}
        class="p-1.5 rounded-md hover:bg-rule-soft transition-colors text-ink-mute"
        aria-label="Close"
      >
        <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
        </svg>
      </button>
    </div>

    {#if selectedJob}
      <div class="p-5 space-y-4">
        <!-- Summary -->
        <div class="grid grid-cols-2 gap-3 text-sm">
          <div>
            <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-0.5">ID</p>
            <p class="font-mono text-ink text-xs break-all">{selectedJob.id}</p>
          </div>
          <div>
            <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-0.5">Status</p>
            <StatusBadge status={selectedJob.status} />
          </div>
          <div>
            <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-0.5">Target</p>
            <p class="text-ink">{selectedJob.target}</p>
          </div>
          <div>
            <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-0.5">Type</p>
            <p class="font-mono text-ink text-xs">{selectedJob.command_type}</p>
          </div>
          <div>
            <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-0.5">Created</p>
            <p class="text-ink-soft text-xs">{fmtDate(selectedJob.created_at)}</p>
          </div>
          <div>
            <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-0.5">Updated</p>
            <p class="text-ink-soft text-xs">{fmtDate(selectedJob.updated_at)}</p>
          </div>
        </div>

        <!-- Error if any -->
        {#if selectedJob.error}
          <div class="rounded-md border border-danger/30 bg-danger-soft p-3 text-xs font-mono text-danger break-all">
            {selectedJob.error}
          </div>
        {/if}

        <!-- Full JSON -->
        <div>
          <p class="text-xs text-ink-mute uppercase tracking-wider font-mono mb-1">Raw JSON</p>
          <pre class="text-xs font-mono bg-paper-warm border border-rule rounded-md p-3 overflow-x-auto whitespace-pre-wrap break-all text-ink-soft">{JSON.stringify(selectedJob, null, 2)}</pre>
        </div>

        <!-- Action feedback -->
        {#if actionError}
          <div class="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-xs text-danger">{actionError}</div>
        {/if}
        {#if actionSuccess}
          <div class="rounded-md border border-success/30 bg-success-soft px-3 py-2 text-xs text-success">{actionSuccess}</div>
        {/if}

        <!-- Actions -->
        <div class="flex items-center gap-3 pt-2">
          <button
            onclick={cancelJob}
            disabled={cancelLoading || ['cancelled', 'completed', 'done', 'failed'].includes(selectedJob.status?.toLowerCase())}
            class="px-4 py-2 rounded-md border border-danger/40 bg-danger-soft text-danger text-sm font-medium hover:bg-danger/10 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            {cancelLoading ? 'Cancelling…' : 'Cancel Job'}
          </button>
          <button
            onclick={retryJob}
            disabled={retryLoading}
            class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-paper-warm disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            {retryLoading ? 'Retrying…' : 'Retry'}
          </button>
        </div>
      </div>
    {/if}
  </dialog>
{/if}
