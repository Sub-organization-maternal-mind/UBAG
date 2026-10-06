<script lang="ts">
  import { onMount } from 'svelte';
  import { EM_DASH, fmtCount, fmtTime } from '$lib/api/fleet';
  import { loadVoicePanel, voicePlacement, voiceQueueReason, yesNo } from '$lib/api/voice';
  import { pollWhileVisible } from '$lib/poll';
  import type { VoicePanel } from '$lib/api/voice';
  import StatusBadge from './StatusBadge.svelte';

  // Live voice per target (supported / configured / verified / available) and this
  // tenant's voice sessions. Null until both reads answer 200, so a gateway without
  // voice (501), an older one (404) or a failed read shows no panel at all.
  let panel = $state<VoicePanel | null>(null);

  async function load(force = false) {
    panel = await loadVoicePanel(force);
  }

  onMount(() => {
    // Same 45 s visible-tab cadence as the Browser page.
    return pollWhileVisible(() => load(), 45_000);
  });
</script>

{#if panel}
  <section class="space-y-3" aria-labelledby="voice-heading">
    <div class="flex items-center justify-between">
      <h2 id="voice-heading" class="text-sm font-semibold text-ink uppercase tracking-wider">Voice Sessions</h2>
      <button onclick={() => load(true)} class="text-xs text-accent-deep hover:underline">Refresh</button>
    </div>

    {#if panel.targets.length === 0}
      <p class="text-xs text-ink-mute italic">No target declares live voice.</p>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-wrap" role="region" aria-label="Voice capabilities by target" tabindex="0">
        <table class="w-full text-xs">
          <thead class="thead">
            <tr>
              <th class="th">Target</th>
              <th class="th">Supported</th>
              <th class="th">Configured</th>
              <th class="th">Verified</th>
              <th class="th">Available</th>
              <th class="th">Free accounts</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each panel.targets as row (row.target)}
              <tr class="transition-colors hover:bg-paper-soft/70">
                <td class="td font-mono text-ink" title={row.display_name}>{row.target}</td>
                <td class="td">{yesNo(row.voice.supported)}</td>
                <td class="td">{yesNo(row.voice.configured)}</td>
                <td class="td" title={row.voice.verified_note}>{yesNo(row.voice.verified)}</td>
                <td class="td">{yesNo(row.voice.available)}</td>
                <td class="td">{fmtCount(row.voice.free_resources)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    {#if panel.sessions.length === 0}
      <p class="text-xs text-ink-mute italic">No voice sessions.</p>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-wrap" role="region" aria-label="Voice sessions" tabindex="0">
        <table class="w-full text-xs">
          <thead class="thead">
            <tr>
              <th class="th">Session</th>
              <th class="th">Target</th>
              <th class="th">Mode</th>
              <th class="th">Status</th>
              <th class="th">Queued reason</th>
              <th class="th">Placement</th>
              <th class="th">Updated</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each panel.sessions as s (s.session_id)}
              {@const reason = voiceQueueReason(s)}
              <tr class="transition-colors hover:bg-paper-soft/70">
                <td class="td font-mono text-ink" title={s.session_id}>{s.session_id.slice(0, 18)}</td>
                <td class="td font-mono">{s.target || EM_DASH}</td>
                <td class="td">{s.mode || EM_DASH}</td>
                <td class="td">
                  <div class="flex flex-wrap items-center gap-2">
                    <StatusBadge status={s.status} />
                    {#if s.muted}<span class="text-ink-mute">Muted</span>{/if}
                  </div>
                </td>
                <td class="td" title={reason?.hint ?? ''}>{reason ? reason.label : EM_DASH}</td>
                <td class="td font-mono">{voicePlacement(s)}</td>
                <td class="td whitespace-nowrap">{fmtTime(s.updated_at)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
{/if}
