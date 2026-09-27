<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import Modal from '$lib/components/Modal.svelte';
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
  }

  function fmtDate(s?: string): string {
    if (!s) return '—';
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <PageHeader title="Templates" subtitle="Reusable job templates registered on the gateway.">
    {#snippet actions()}
      <button onclick={() => load()} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <input
    type="search"
    bind:value={filter}
    placeholder="Filter by ID, command type, description…"
    class="input max-w-sm"
  />

  {#if loading}
    <SkeletonTable rows={6} cols={5} />
  {:else if denied}
    <DeniedPanel resource="templates" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if filtered.length === 0}
    <EmptyState message="No templates found." hint={filter ? 'Try clearing the filter.' : ''} />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">ID</th>
            <th class="th">Command Type</th>
            <th class="th">Description</th>
            <th class="th">Created</th>
            <th class="th">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each filtered as template (template.id)}
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink-mute">{template.id.slice(0, 8)}…</td>
              <td class="td font-mono text-xs font-medium text-ink">{template.command_type}</td>
              <td class="td text-xs max-w-xs truncate" title={template.description}>{template.description ?? '—'}</td>
              <td class="td text-xs text-ink-mute whitespace-nowrap">{fmtDate(template.created_at)}</td>
              <td class="td">
                <button onclick={() => previewRender(template)} class="btn btn-sm border border-accent-deep/40 text-accent-deep hover:bg-accent-soft">
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
<Modal bind:open={previewOpen} title="Render Preview" width="lg">
  <div class="space-y-3">
    <p class="font-mono text-xs text-ink-mute">{previewTemplateId}</p>
    {#if previewLoading}
      <div class="text-sm text-ink-mute animate-pulse">Rendering…</div>
    {:else if previewError}
      <div class="rounded-md border border-danger/30 bg-danger-soft p-3 text-xs text-danger">{previewError}</div>
    {:else}
      <pre class="text-xs font-mono bg-paper-warm border border-rule rounded-md p-3 overflow-x-auto whitespace-pre-wrap break-all text-ink-soft max-h-96">{previewContent || '(empty response)'}</pre>
    {/if}
  </div>
  {#snippet footer()}
    <button onclick={closePreview} class="btn btn-secondary">Close</button>
  {/snippet}
</Modal>
