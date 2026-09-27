<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { normalizeJobs } from '$lib/api/jobs';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import UpdatedAgo from '$lib/components/UpdatedAgo.svelte';
  import { pollWhileVisible } from '$lib/poll';
  import type { Job, JobsResponse } from '$lib/api/types';
  import { FAILED_STATES } from '$lib/api/statuses';

  // Real terminal-failure statuses from the contract vocabulary
  // (failed_retryable, failed_terminal, dead_letter, timed_out).
  const TERMINAL_STATES = FAILED_STATES;

  let allJobs = $state<Job[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);

  // Per-row requeue state: maps job ID → { loading, success, error }
  let requeueState = $state<Record<string, { loading: boolean; success: string | null; error: string | null }>>({});

  let failed = $derived(
    allJobs.filter((j) => TERMINAL_STATES.has(j.status?.toLowerCase()))
  );

  let lastUpdated = $state<Date | null>(null);

  async function load(silent = false) {
    if (!silent) {
      loading = true;
      error = null;
      denied = false;
    }
    const res = await api.get<JobsResponse>('/v1/jobs?limit=100');
    if (!silent) loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    allJobs = normalizeJobs(res.data?.jobs);
    lastUpdated = new Date();
  }

  // Requeue retries the failed job itself (POST /v1/jobs/{id}/retry) instead
  // of minting a brand-new job — the gateway links the retry via retry_of.
  async function requeue(job: Job) {
    requeueState = {
      ...requeueState,
      [job.id]: { loading: true, success: null, error: null },
    };

    const res = await api.post<{ job: Job }>(`/v1/jobs/${job.id}/retry`, {
      reason: 'dashboard-requeue',
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

  onMount(() => {
    load();
    const stopPolling = pollWhileVisible(() => load(true), 45_000);
    return stopPolling;
  });
</script>

<div class="space-y-4">
  <PageHeader title="Failed / DLQ" subtitle="Jobs in terminal failure states: {[...FAILED_STATES].join(', ')}">
    {#snippet actions()}
      <UpdatedAgo at={lastUpdated} />
      <button onclick={() => load()} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  {#if loading}
    <SkeletonTable rows={5} cols={7} />
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

    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">ID</th>
            <th class="th">Target</th>
            <th class="th">Command Type</th>
            <th class="th">Status</th>
            <th class="th">Error</th>
            <th class="th">Created At</th>
            <th class="th">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each failed as job (job.id)}
            {@const rs = requeueState[job.id]}
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink-mute">{job.id.slice(0, 8)}…</td>
              <td class="td max-w-[8rem] truncate text-ink" title={job.target}>{job.target}</td>
              <td class="td font-mono text-xs">{job.command_type}</td>
              <td class="td"><StatusBadge status={job.status} /></td>
              <td class="td text-xs font-mono text-danger max-w-[20rem] truncate" title={job.error}>
                {truncate(job.error, 80)}
              </td>
              <td class="td text-xs text-ink-mute whitespace-nowrap">{fmtDate(job.created_at)}</td>
              <td class="td">
                <div class="flex items-center gap-2">
                  <button onclick={() => requeue(job)} disabled={rs?.loading} class="btn btn-secondary btn-sm">
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
