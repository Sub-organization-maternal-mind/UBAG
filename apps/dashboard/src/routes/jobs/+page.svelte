<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import { normalizeJobs } from '$lib/api/jobs';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import AttachmentPicker from '$lib/components/AttachmentPicker.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import UpdatedAgo from '$lib/components/UpdatedAgo.svelte';
  import { pollWhileVisible } from '$lib/poll';
  import type { SelectedAttachment } from '$lib/attachments';
  import type { BrowserContext, Job, JobCreateResponse, JobEnvelope, JobsResponse, Template } from '$lib/api/types';
  import { isTerminalStatus } from '$lib/api/statuses';

  const API_VERSION = '2026-05-22';
  const PROVIDERS = [
    { key: 'chatgpt_web', label: 'ChatGPT' },
    { key: 'gemini_web', label: 'Gemini' },
    { key: 'deepseek_web', label: 'DeepSeek' },
    { key: 'duckai_web', label: 'Duck.ai' },
  ];

  let items = $state<Job[]>([]);
  let templates = $state<Template[]>([]);
  let contexts = $state<BrowserContext[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let nextCursor = $state<string | undefined>(undefined);
  let prevCursors = $state<string[]>([]);
  let currentCursor = $state<string | undefined>(undefined);

  let lastUpdated = $state<Date | null>(null);

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
  let createAttachments = $state<SelectedAttachment[]>([]);
  let attachmentError = $state<string | null>(null);
  let attachmentPicker = $state<{ clear: () => void } | null>(null);

  let filtered = $derived.by(() => {
    const q = filterQuery.trim().toLowerCase();
    if (!q) return items;
    return items.filter((j) =>
      `${j.id ?? ''} ${j.target ?? ''} ${j.command_type ?? ''} ${j.status ?? ''} ${j.error ?? ''}`
        .toLowerCase()
        .includes(q)
    );
  });
  let filter = $state('');
  let filterQuery = $state('');
  let filterTimer: ReturnType<typeof setTimeout> | undefined;
  function onFilterInput(e: Event) {
    const value = (e.currentTarget as HTMLInputElement).value;
    filter = value;
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => (filterQuery = value), 120);
  }
  let providerState = $derived(
    Object.fromEntries(contexts.map((ctx) => [ctx.target_id, ctx.login_state ?? 'unknown']))
  );
  let matchingTemplates = $derived(
    templates.filter((template) => !createCommandType || template.command_type === createCommandType)
  );

  let loadSeq = 0;
  async function load(cursor?: string, silent = false) {
    const seq = ++loadSeq;
    if (!silent) {
      loading = true;
      error = null;
      denied = false;
    }
    const path = cursor ? `/v1/jobs?cursor=${encodeURIComponent(cursor)}&limit=20` : '/v1/jobs?limit=20';
    const res = await api.get<JobsResponse>(path);
    if (seq !== loadSeq) return; // a newer load superseded this one
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = normalizeJobs(res.data?.jobs);
    nextCursor = res.data?.next_cursor;
    lastUpdated = new Date();
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
    if (attachmentError) {
      createError = attachmentError;
      return;
    }
    createLoading = true;
    let res;
    if (createAttachments.length > 0) {
      // Multipart one-shot: job envelope + one file part per attachment (part
      // name === key). Declare the manifest inline so the gateway can validate it.
      const envelope = buildJobEnvelope();
      envelope.job.input = {
        ...envelope.job.input,
        attachments: createAttachments.map((attachment) => ({
          key: attachment.key,
          filename: attachment.key,
          content_type: attachment.contentType,
          kind: attachment.kind,
        })),
      };
      const form = new FormData();
      form.append('job', new Blob([JSON.stringify(envelope)], { type: 'application/json' }));
      for (const attachment of createAttachments) {
        form.append(attachment.key, attachment.file, attachment.key);
      }
      res = await api.postMultipart<JobCreateResponse>('/v1/jobs', form);
    } else {
      res = await api.post<JobCreateResponse>('/v1/jobs', buildJobEnvelope());
    }
    createLoading = false;
    if (res.error) {
      createError = res.error;
      return;
    }
    createSuccess = `Created job ${res.data?.job_id?.slice(0, 8) ?? ''}`;
    createPrompt = '';
    attachmentPicker?.clear();
    await load(currentCursor);
  }

  // --- Batch submit (POST /v1/jobs/batch, blueprint A§10/A§19.2) ---
  interface BatchOutcome {
    index: number;
    status: string;
    job_id?: string;
    idempotent_replay?: boolean;
    error?: { code?: string; message?: string };
  }
  let batchText = $state('');
  let batchLoading = $state(false);
  let batchError = $state<string | null>(null);
  let batchResults = $state<BatchOutcome[] | null>(null);

  function parseBatchLines(): { prompt: string; command_type: string }[] {
    return batchText
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line.length > 0 && !line.startsWith('#'))
      .map((line) => {
        // "command_type | prompt" — defaults to chat.prompt when no pipe present.
        const sep = line.indexOf('|');
        if (sep === -1) return { prompt: line, command_type: createCommandType.trim() || 'chat.prompt' };
        return { command_type: line.slice(0, sep).trim(), prompt: line.slice(sep + 1).trim() };
      });
  }

  async function submitBatch() {
    batchError = null;
    batchResults = null;
    const entries = parseBatchLines();
    if (entries.length === 0) {
      batchError = 'Add one job per line ("command type | prompt").';
      return;
    }
    if (entries.length > 100) {
      batchError = 'A batch is capped at 100 jobs.';
      return;
    }
    batchLoading = true;
    const res = await api.post<{ accepted?: number; rejected?: number; results?: BatchOutcome[] }>(
      '/v1/jobs/batch',
      {
        api_version: API_VERSION,
        jobs: entries.map((entry) => ({
          api_version: API_VERSION,
          client: {
            app_id: 'ubag-dashboard',
            app_version: '0.0.0',
            sdk: { name: 'ubag-dashboard', version: '0.0.0' },
          },
          job: {
            target: createTarget,
            command_type: entry.command_type,
            input: { prompt: entry.prompt },
            options: { priority: 'normal', return_mode: 'final' },
          },
        })),
      }
    );
    batchLoading = false;
    if (res.error) {
      batchError = `${res.error} (HTTP ${res.status})`;
      return;
    }
    batchResults = res.data?.results ?? [];
  }

  function fmtDate(s: string): string {
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  onMount(() => {
    load();
    loadSupportData();
    // Gentle auto-refresh while the tab is visible; silent (no skeleton flash).
    const stopPolling = pollWhileVisible(() => load(currentCursor, true), 45_000);
    return stopPolling;
  });
</script>

<div class="space-y-4">
  <PageHeader title="Jobs" subtitle="Submit, inspect and manage provider jobs on the gateway queue.">
    {#snippet actions()}
      <UpdatedAgo at={lastUpdated} />
      <button onclick={() => load(currentCursor)} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <form onsubmit={(e) => { e.preventDefault(); createJob(); }} class="card space-y-4">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <h2 class="text-sm font-display font-semibold text-ink">Submit Provider Job</h2>
      <div class="text-xs text-ink-mute font-mono">ChatGPT → Gemini → DeepSeek → Duck.ai</div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-[1.2fr_1fr_1fr]">
      <div class="sm:col-span-2 lg:col-span-1">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Provider</span>
        <div class="grid grid-cols-2 sm:grid-cols-4 rounded-md border border-rule overflow-hidden bg-paper" role="group" aria-label="Provider">
          {#each PROVIDERS as provider (provider.key)}
            <button
              type="button"
              onclick={() => { createTarget = provider.key; }}
              class="min-w-0 px-3 py-2 text-sm transition-colors [overflow-wrap:anywhere]"
              class:bg-accent-soft={createTarget === provider.key}
              class:text-accent-deep={createTarget === provider.key}
              class:font-medium={createTarget === provider.key}
              class:text-ink-soft={createTarget !== provider.key}
            >
              <span class="block truncate">{provider.label}</span>
              <span class="block truncate text-[11px] font-mono text-ink-mute mt-0.5">{providerState[provider.key] ?? 'unknown'}</span>
            </button>
          {/each}
        </div>
      </div>

      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Command Type</span>
        <input bind:value={createCommandType} class="input" />
      </label>

      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Template</span>
        <select bind:value={createTemplateId} class="input">
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
        class="input resize-y"
      ></textarea>
    </label>

    <div class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Attachments</span>
      <AttachmentPicker
        bind:this={attachmentPicker}
        disabled={createLoading}
        loading={createLoading}
        error={attachmentError}
        success={createSuccess}
        onchange={(attachments, validationError) => {
          createAttachments = attachments;
          attachmentError = validationError;
          if (!validationError) createError = null;
        }}
      />
    </div>

    <div class="flex items-center gap-3 flex-wrap">
      <button type="submit" disabled={createLoading} class="btn btn-primary">
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

  <!-- Batch submit -->
  <section aria-labelledby="batch-heading" class="card space-y-3">
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <h2 id="batch-heading" class="text-sm font-display font-semibold text-ink">Batch Submit</h2>
      <div class="text-xs text-ink-mute font-mono">one job per line · "command type | prompt" · max 100 · target = {createTarget}</div>
    </div>
    <textarea
      bind:value={batchText}
      rows="5"
      placeholder={"chat.prompt | Summarize the incident report\nchat.prompt | Draft the follow-up email\nmock.complete | UBAG_BATCH_CHECK"}
      class="input resize-y font-mono"
      aria-label="Batch job lines"
    ></textarea>
    <div class="flex items-center gap-3 flex-wrap">
      <button onclick={() => submitBatch()} disabled={batchLoading} class="btn btn-primary">
        {batchLoading ? 'Submitting…' : 'Submit Batch'}
      </button>
      {#if batchError}
        <span class="text-xs text-danger">{batchError}</span>
      {/if}
    </div>
    {#if batchResults}
      <div class="table-wrap">
        <table class="w-full text-xs">
          <thead class="thead">
            <tr>
              <th class="th">#</th>
              <th class="th">Status</th>
              <th class="th">Job</th>
              <th class="th">Error</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each batchResults as outcome (outcome.index)}
              <tr>
                <td class="td font-mono text-ink-mute">{outcome.index}</td>
                <td class="td">
                  {#if outcome.status === 'accepted'}
                    <span class="text-success font-medium">accepted</span>
                  {:else}
                    <span class="text-danger font-medium">rejected</span>
                  {/if}
                </td>
                <td class="td font-mono text-ink-soft">{outcome.job_id ?? '—'}</td>
                <td class="td">{outcome.error?.code ?? ''} {outcome.error?.message ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <!-- Filter -->
  <input
    type="search"
    value={filter}
    oninput={onFilterInput}
    placeholder="Filter by status, target, type…"
    class="input max-w-sm"
  />

  {#if loading}
    <SkeletonTable rows={8} cols={6} />
  {:else if denied}
    <DeniedPanel resource="jobs" />
  {:else if error}
    <ErrorPanel message={error} retry={() => load(currentCursor)} />
  {:else if filtered.length === 0}
    <EmptyState message="No jobs found." hint={filter ? 'Try clearing the filter.' : ''} />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">ID</th>
            <th class="th">Target</th>
            <th class="th">Command Type</th>
            <th class="th">Status</th>
            <th class="th">Created At</th>
            <th class="th">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as job (job.id)}
            <tr
              class="cursor-pointer transition-colors hover:bg-paper-soft/70"
              onclick={() => openDrawer(job)}
              tabindex="0"
              role="button"
              aria-label="View job {job.id}"
              onkeydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  openDrawer(job);
                }
              }}
            >
              <td class="td font-mono text-xs text-ink-mute">{job.id.slice(0, 8)}…</td>
              <td class="td max-w-[10rem] truncate text-ink" title={job.target}>{job.target}</td>
              <td class="td font-mono text-xs">{job.command_type}</td>
              <td class="td"><StatusBadge status={job.status} /></td>
              <td class="td whitespace-nowrap text-xs text-ink-mute">{fmtDate(job.created_at)}</td>
              <td class="td">
                <button
                  onclick={(e) => { e.stopPropagation(); openDrawer(job); }}
                  class="btn-link text-xs"
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
      <button onclick={goPrev} disabled={prevCursors.length === 0} class="btn btn-secondary btn-sm">
        ← Prev
      </button>
      <button onclick={goNext} disabled={!nextCursor} class="btn btn-secondary btn-sm">
        Next →
      </button>
    </div>
  {/if}
</div>

<!-- Job detail drawer (dialog) -->
{#if drawerOpen}
  <dialog
    bind:this={dialogEl}
    class="fixed inset-y-0 right-0 m-0 ml-auto h-dvh w-full max-w-lg bg-paper border-l border-rule shadow-2xl overflow-y-auto p-0 backdrop:bg-ink/40"
    aria-label="Job details"
    onclose={closeDrawer}
  >
    <div class="sticky top-0 z-10 flex items-center justify-between px-5 py-4 border-b border-rule bg-paper-soft">
      <h2 class="text-lg font-display font-semibold text-ink">Job Details</h2>
      <button
        onclick={closeDrawer}
        class="flex h-9 w-9 items-center justify-center rounded-md text-ink-mute transition-colors hover:bg-rule-soft hover:text-ink"
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
            disabled={cancelLoading || isTerminalStatus(selectedJob.status)}
            class="btn btn-danger"
          >
            {cancelLoading ? 'Cancelling…' : 'Cancel Job'}
          </button>
          <button onclick={retryJob} disabled={retryLoading} class="btn btn-secondary">
            {retryLoading ? 'Retrying…' : 'Retry'}
          </button>
        </div>
      </div>
    {/if}
  </dialog>
{/if}
