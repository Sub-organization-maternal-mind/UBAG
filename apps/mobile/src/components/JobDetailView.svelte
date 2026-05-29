<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { settings } from "../lib/settings";
  import { getJob, listJobEvents } from "../lib/api";
  import type { JobEvent, JobResponse, JobStatus } from "../lib/types";
  import { eventTone, formatTimestamp, humanize, relativeTime } from "../lib/format";
  import StatusBadge from "./StatusBadge.svelte";
  import AsyncState from "./AsyncState.svelte";

  export let jobId: string;
  export let onBack: () => void;

  let job: JobResponse | null = null;
  let events: JobEvent[] = [];
  let loading = true;
  let error: string | null = null;
  let lastSequence = 0;
  let live = false;
  let timer: ReturnType<typeof setInterval> | undefined;

  const TERMINAL: JobStatus[] = [
    "completed",
    "completed_with_warnings",
    "failed_terminal",
    "dead_letter",
    "cancelled",
    "timed_out",
  ];

  function isTerminal(status: JobStatus | undefined): boolean {
    return !!status && TERMINAL.includes(status);
  }

  async function loadJob() {
    try {
      job = await getJob($settings.gatewayUrl, jobId);
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to load job";
    }
  }

  async function loadEvents() {
    try {
      const res = await listJobEvents($settings.gatewayUrl, jobId, {
        afterSequence: lastSequence,
        limit: 100,
      });
      if (res.events.length > 0) {
        // Append only genuinely new events (defensive against overlap).
        const incoming = res.events.filter((e) => e.sequence > lastSequence);
        if (incoming.length > 0) {
          events = [...events, ...incoming];
          lastSequence = events[events.length - 1].sequence;
        }
      }
    } catch (err) {
      if (!error) {
        error = err instanceof Error ? err.message : "Failed to load events";
      }
    }
  }

  async function tick() {
    await Promise.all([loadJob(), loadEvents()]);
    if (isTerminal(job?.status) || !$settings.liveTimeline) {
      stopLive();
    }
  }

  function startLive() {
    if (timer || !$settings.liveTimeline) {
      return;
    }
    live = true;
    timer = setInterval(tick, Math.max(3, Math.min($settings.refreshSeconds, 10)) * 1000);
  }

  function stopLive() {
    live = false;
    if (timer) {
      clearInterval(timer);
      timer = undefined;
    }
  }

  function dataPreview(data: Record<string, unknown>): string {
    const keys = Object.keys(data);
    if (keys.length === 0) {
      return "";
    }
    try {
      const json = JSON.stringify(data, null, 2);
      return json.length > 600 ? `${json.slice(0, 600)}…` : json;
    } catch {
      return "";
    }
  }

  onMount(async () => {
    await Promise.all([loadJob(), loadEvents()]);
    loading = false;
    if (!isTerminal(job?.status)) {
      startLive();
    }
  });
  onDestroy(stopLive);
</script>

<section aria-labelledby="job-title">
  <button class="btn secondary" style="width:auto;margin-bottom:var(--space-3)" on:click={onBack}>
    ‹ Back to jobs
  </button>

  <div class="row-between" style="margin-bottom: var(--space-2)">
    <div>
      <p class="section-kicker">Job</p>
      <h2 id="job-title" class="view-title mono" style="margin-bottom:0;font-size:var(--text-lg)">{jobId}</h2>
    </div>
    {#if job}<StatusBadge status={job.status} />{/if}
  </div>

  {#if loading}
    <AsyncState loading={true} />
  {:else if error && !job}
    <AsyncState error={error} onRetry={tick} />
  {:else if job}
    <div class="card">
      <dl class="kv">
        <dt>Target</dt><dd>{job.target}</dd>
        <dt>Status</dt><dd>{humanize(job.status)}</dd>
        <dt>Created</dt><dd>{formatTimestamp(job.created_at)}</dd>
        <dt>Updated</dt><dd>{relativeTime(job.updated_at ?? job.created_at)}</dd>
        <dt>Trace</dt><dd>{job.trace_id}</dd>
      </dl>
    </div>

    <div class="row-between" style="margin-bottom: var(--space-2)">
      <h3>Event timeline</h3>
      {#if live}
        <span class="live-pill"><span class="pulse" aria-hidden="true"></span> LIVE</span>
      {:else if !isTerminal(job.status)}
        <button class="icon-btn" aria-label="Resume live updates" on:click={startLive}>▶</button>
      {/if}
    </div>

    {#if events.length === 0}
      <AsyncState empty={true} emptyTitle="No events yet" emptyDetail="The job has not emitted events." />
    {:else}
      <div class="timeline">
        {#each events as event (event.event_id)}
          <div class="timeline-item">
            <span class="marker" style="background: var(--tone-{eventTone(event.type)}, var(--color-ink-mute))" aria-hidden="true"></span>
            <div>
              <div class="event-type">{humanize(event.type)}</div>
              <div class="event-meta">#{event.sequence} · {formatTimestamp(event.created_at)}</div>
              {#if dataPreview(event.data)}
                <pre class="event-data">{dataPreview(event.data)}</pre>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>
