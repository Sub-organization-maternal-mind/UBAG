<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { settings } from "../lib/settings";
  import { listJobs } from "../lib/api";
  import { JOB_STATUSES } from "../lib/types";
  import type { JobResponse, JobStatus } from "../lib/types";
  import { relativeTime } from "../lib/format";
  import StatusBadge from "./StatusBadge.svelte";
  import AsyncState from "./AsyncState.svelte";

  export let onOpen: (jobId: string) => void;

  let jobs: JobResponse[] = [];
  let statusFilter: JobStatus | "" = "";
  let loading = true;
  let error: string | null = null;
  let timer: ReturnType<typeof setInterval> | undefined;

  async function load() {
    loading = jobs.length === 0;
    error = null;
    try {
      const res = await listJobs($settings.gatewayUrl, { status: statusFilter, limit: 50 });
      jobs = res.jobs;
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to load jobs";
    } finally {
      loading = false;
    }
  }

  function onFilterChange() {
    jobs = [];
    load();
  }

  onMount(() => {
    load();
    timer = setInterval(load, Math.max(5, $settings.refreshSeconds) * 1000);
  });
  onDestroy(() => timer && clearInterval(timer));
</script>

<section aria-labelledby="jobs-title">
  <div class="row-between" style="margin-bottom: var(--space-3)">
    <div>
      <p class="section-kicker">Work</p>
      <h2 id="jobs-title" class="view-title" style="margin-bottom:0">Jobs</h2>
    </div>
    <button class="icon-btn" aria-label="Refresh" on:click={load}>↻</button>
  </div>

  <div class="toolbar">
    <label for="status-filter" class="sr-only" style="position:absolute;left:-9999px">Filter by status</label>
    <select id="status-filter" bind:value={statusFilter} on:change={onFilterChange}>
      <option value="">All statuses</option>
      {#each JOB_STATUSES as status}
        <option value={status}>{status}</option>
      {/each}
    </select>
  </div>

  {#if loading}
    <AsyncState loading={true} />
  {:else if error}
    <AsyncState error={error} onRetry={load} />
  {:else if jobs.length === 0}
    <AsyncState
      empty={true}
      emptyTitle="No jobs"
      emptyDetail="No jobs match the current filter."
    />
  {:else}
    <div class="list">
      {#each jobs as job (job.job_id)}
        <button class="list-item" on:click={() => onOpen(job.job_id)}>
          <div>
            <div class="primary">{job.job_id}</div>
            <div class="secondary">
              {job.target} · {relativeTime(job.updated_at ?? job.created_at)}
            </div>
          </div>
          <StatusBadge status={job.status} />
        </button>
      {/each}
    </div>
  {/if}
</section>
