<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import type { Template } from '$lib/api/types';

  let items = $state<Template[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);
  let filter = $state('');

  // Preview modal
  let previewOpen = $state(false);
  let previewLoading = $state(false);
  let previewError = $state<string | null>(null);
  let previewContent = $state<string>('');
  let previewTemplateId = $state<string>('');
  let dialogEl = $state<HTMLDialogElement | null>(null);

  interface TemplateRenderResponse {
    rendered?: string;
    template_id?: string;
  }

  let filtered = $derived(
    filter
      ? items.filter(t =>
          (t.command_type ?? '').toLowerCase().includes(filter.toLowerCase()) ||
          (t.description ?? '').toLowerCase().includes(filter.toLowerCase()) ||
          t.id.toLowerCase().includes(filter.toLowerCase())
        )
      : items
  );

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get('/v1/templates');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    items = listOf<Template>(res);
  }

  async function previewRender(template: Template) {
    previewTemplateId = template.id;
    previewError = null;
    previewContent = '';
    previewLoading = true;
    previewOpen = true;
    requestAnimationFrame(() => { dialogEl?.showModal(); });

    const res = await api.post<TemplateRenderResponse>(`/v1/templates/${template.id}/render`, { vars: {} });
    previewLoading = false;
    if (res.error) {
      previewError = res.error;
      return;
    }
    previewContent = res.data?.rendered ?? '';
  }

  function closePreview() {
    previewOpen = false;
    dialogEl?.close();
  }

  function fmtDate(s?: string): string {
    if (!s) return '—';
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Templates</h1>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <input
    type="search"
    bind:value={filter}
    placeholder="Filter by ID, command type, description…"
    class="w-full max-w-sm px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
  />

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="templates" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No templates found." hint={filter ? 'Try clearing the filter.' : ''} />
  {:else}
    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Command Type</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Description</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Created</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as template (template.id)}
            <tr class="hover:bg-paper-soft transition-colors">
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{template.id.slice(0, 8)}…</td>
              <td class="px-4 py-2.5 text-ink font-medium font-mono text-xs">{template.command_type}</td>
              <td class="px-4 py-2.5 text-ink-soft text-xs max-w-xs truncate" title={template.description}>{template.description ?? '—'}</td>
              <td class="px-4 py-2.5 text-ink-mute text-xs">{fmtDate(template.created_at)}</td>
              <td class="px-4 py-2.5">
                <button
                  onclick={() => previewRender(template)}
                  class="text-xs px-2.5 py-1 rounded-md border border-accent-deep/40 text-accent-deep hover:bg-accent-soft transition-colors"
                >
                  Preview render
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<!-- Preview modal -->
{#if previewOpen}
  <dialog
    bind:this={dialogEl}
    class="w-full max-w-2xl rounded-lg border border-rule bg-paper shadow-2xl p-0 backdrop:bg-ink/40"
    aria-label="Template render preview"
    onclose={closePreview}
  >
    <div class="flex items-center justify-between px-5 py-4 border-b border-rule bg-paper-soft">
      <h2 class="text-lg font-display font-semibold text-ink">
        Preview — <span class="font-mono text-ink-mute text-sm">{previewTemplateId}</span>
      </h2>
      <button
        onclick={closePreview}
        class="p-1.5 rounded-md hover:bg-rule-soft transition-colors text-ink-mute"
        aria-label="Close"
      >
        <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
        </svg>
      </button>
    </div>
    <div class="p-5">
      {#if previewLoading}
        <div class="text-ink-mute text-sm">Rendering…</div>
      {:else if previewError}
        <div class="rounded-md border border-danger/30 bg-danger-soft p-3 text-xs text-danger">{previewError}</div>
      {:else}
        <pre class="text-xs font-mono bg-paper-warm border border-rule rounded-md p-3 overflow-x-auto whitespace-pre-wrap break-all text-ink-soft max-h-96">{previewContent || '(empty response)'}</pre>
      {/if}
    </div>
    <div class="px-5 py-3 border-t border-rule flex justify-end">
      <button
        onclick={closePreview}
        class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-paper-warm transition-colors"
      >
        Close
      </button>
    </div>
  </dialog>
{/if}
