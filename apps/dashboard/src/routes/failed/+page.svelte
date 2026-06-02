<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import type { Job, JobsResponse } from '$lib/api/types';

  const TERMINAL_STATES = new Set(['failed', 'error', 'dead', 'dlq']);

  let allJobs = $state<Job[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);

  // Per-row requeue state: maps job ID → { loading, success, error }
  let requeueState = $state<Record<string, { loading: boolean; success: string | null; error: string | null }>>({});

  let failed = $derived(
    allJobs.filter((j) => TERMINAL_STATES.has(j.status?.toLowerCase()))
  );

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get<JobsResponse>('/v1/jobs?limit=200');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    allJobs = res.data?.jobs ?? [];
  }

  async function requeue(job: Job) {
    requeueState = {
      ...requeueState,
      [job.id]: { loading: true, success: null, error: null },
    };

    const res = await api.post<{ job: Job }>('/v1/jobs', {
      job: {
        target: job.target,
        command_type: job.command_type,
        input: (job as unknown as Record<string, unknown>)['input'] ?? null,
      },
      client: {
        app_id: 'ubag-dashboard',
        app_version: '1.0.0',
        sdk: { name: 'dashboard', version: '1.0.0' },
      },
    });

    if (res.error) {
      requeueState = {
        ...requeueState,
        [job.id]: { loading: false, success: null, error: res.error },
      };
    } else {
      const newId = res.data?.job?.id?.slice(0, 8) ?? '?';
      requeueState = {
        ...requeueState,
        [job.id]: { loading: false, success: `Queued as ${newId}`, error: null },
      };
    }
  }

  function fmtDate(s: string): string {
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  function truncate(s: string | undefined, n: number): string {
    if (!s) return '—';
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-display font-bold text-ink">Failed / DLQ</h1>
      <p class="text-xs text-ink-mute mt-0.5">Jobs in terminal failure states: failed, error, dead, dlq</p>
    </div>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  {#if loading}
    <div class="text-ink-mute text-sm animate-pulse">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="jobs" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if failed.length === 0}
    <EmptyState
      message="No failed jobs."
      hint="Jobs with status failed, error, dead, or dlq will appear here."
    />
  {:else}
    <!-- Summary strip -->
    <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-2.5 text-sm text-danger font-medium">
      {failed.length} job{failed.length === 1 ? '' : 's'} in terminal failure state
    </div>

    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Target</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Command Type</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Error</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Created At</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each failed as job (job.id)}
            {@const rs = requeueState[job.id]}
            <tr class="hover:bg-paper-soft transition-colors">
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{job.id.slice(0, 8)}…</td>
              <td class="px-4 py-2.5 text-ink-soft max-w-[8rem] truncate" title={job.target}>{job.target}</td>
              <td class="px-4 py-2.5 font-mono text-xs text-ink-soft">{job.command_type}</td>
              <td class="px-4 py-2.5"><StatusBadge status={job.status} /></td>
              <td class="px-4 py-2.5 text-xs text-danger font-mono max-w-[20rem]" title={job.error}>
                {truncate(job.error, 80)}
              </td>
              <td class="px-4 py-2.5 text-ink-mute text-xs whitespace-nowrap">{fmtDate(job.created_at)}</td>
              <td class="px-4 py-2.5">
                <div class="flex items-center gap-2">
                  <button
                    onclick={() => requeue(job)}
                    disabled={rs?.loading}
                    class="px-3 py-1 rounded-md border border-rule bg-paper-soft text-xs font-medium text-ink hover:bg-paper-warm disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
                  >
                    {rs?.loading ? 'Queuing…' : 'Requeue'}
                  </button>
                </div>
                <!-- Inline feedback -->
                {#if rs?.success}
                  <p class="mt-1 text-xs text-success">{rs.success}</p>
                {/if}
                {#if rs?.error}
                  <p class="mt-1 text-xs text-danger">{rs.error}</p>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
