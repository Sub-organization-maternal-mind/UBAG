<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { settings } from "../lib/settings";
  import { listAudit, listWebhooks } from "../lib/api";
  import { formatTimestamp, relativeTime, eventTone } from "../lib/format";
  import AsyncState from "./AsyncState.svelte";

  type Record_ = Record<string, unknown>;

  let audit: Record_[] = [];
  let webhooks: Record_[] = [];
  let loading = true;
  let error: string | null = null;
  let timer: ReturnType<typeof setInterval> | undefined;

  // Audit records use an open schema; pick the most useful display fields
  // without assuming a fixed shape.
  function pick(record: Record_, keys: string[]): string | undefined {
    for (const key of keys) {
      const value = record[key];
      if (typeof value === "string" && value) {
        return value;
      }
    }
    return undefined;
  }

  function label(record: Record_): string {
    return (
      pick(record, ["action", "event", "type", "audit_event", "name"]) ?? "audit event"
    );
  }

  function when(record: Record_): string | undefined {
    return pick(record, ["created_at", "timestamp", "occurred_at", "time"]);
  }

  function detail(record: Record_): string | undefined {
    return pick(record, ["actor", "subject", "target", "job_id", "message", "summary"]);
  }

  async function load() {
    error = null;
    try {
      const [auditRes, webhookRes] = await Promise.allSettled([
        listAudit($settings.gatewayUrl, 50),
        listWebhooks($settings.gatewayUrl),
      ]);
      if (auditRes.status === "fulfilled") {
        audit = auditRes.value.data;
      } else if (auditRes.reason instanceof Error) {
        error = auditRes.reason.message;
      }
      webhooks = webhookRes.status === "fulfilled" ? webhookRes.value.data : [];
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to load audit feed";
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    load();
    timer = setInterval(load, Math.max(5, $settings.refreshSeconds) * 1000);
  });
  onDestroy(() => timer && clearInterval(timer));
</script>

<section aria-labelledby="alerts-title">
  <div class="row-between" style="margin-bottom: var(--space-3)">
    <div>
      <p class="section-kicker">Signals</p>
      <h2 id="alerts-title" class="view-title" style="margin-bottom:0">Alerts &amp; Audit</h2>
    </div>
    <button class="icon-btn" aria-label="Refresh" on:click={load}>↻</button>
  </div>

  {#if webhooks.length > 0}
    <div class="card">
      <h3>Webhook destinations</h3>
      <p class="muted">{webhooks.length} configured callback{webhooks.length === 1 ? "" : "s"}.</p>
    </div>
  {/if}

  {#if loading && audit.length === 0}
    <AsyncState loading={true} />
  {:else if error && audit.length === 0}
    <AsyncState error={error} onRetry={load} />
  {:else if audit.length === 0}
    <AsyncState
      empty={true}
      emptyTitle="No audit events"
      emptyDetail="The gateway has not recorded audit activity."
    />
  {:else}
    <div class="list">
      {#each audit as record, i (i)}
        <div class="list-item" style="cursor:default">
          <div style="display:flex;gap:var(--space-3);align-items:center;min-width:0">
            <span class="dot" data-tone={eventTone(label(record))} aria-hidden="true"></span>
            <div style="min-width:0">
              <div class="primary" style="word-break:normal">{label(record)}</div>
              <div class="secondary">
                {detail(record) ?? "—"}
              </div>
            </div>
          </div>
          <span class="secondary" title={formatTimestamp(when(record))}>
            {relativeTime(when(record))}
          </span>
        </div>
      {/each}
    </div>
  {/if}
</section>
