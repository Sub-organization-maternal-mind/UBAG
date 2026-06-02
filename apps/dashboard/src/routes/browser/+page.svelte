<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/api/client';
  import { settings } from '$lib/stores/settings';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import type {
    BrowserInstance,
    BrowserContext,
    BrowserTab,
    BrowserSummary,
  } from '$lib/api/types';

  // --- State ---
  let summary = $state<BrowserSummary | null>(null);
  let instances = $state<BrowserInstance[]>([]);
  let contexts = $state<BrowserContext[]>([]);
  let tabs = $state<BrowserTab[]>([]);

  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);

  let selectedInstance = $state<BrowserInstance | null>(null);

  // xterm state
  let terminalEl = $state<HTMLElement | null>(null);
  let term: { writeln: (s: string) => void; dispose: () => void } | null = null;
  let termReady = $state(false);

  // Contexts/tabs for selected instance
  let instanceContexts = $derived(
    selectedInstance
      ? contexts.filter((c) => c.instance_id === selectedInstance!.id)
      : []
  );

  let instanceTabs = $derived(
    selectedInstance
      ? tabs.filter((t) =>
          instanceContexts.some((c) => c.id === t.context_id)
        )
      : []
  );

  // noVNC URL derived from gatewayUrl
  let noVncSrc = $derived.by(() => {
    if (!selectedInstance) return null;
    const gw = $settings.gatewayUrl.replace(/\/+$/, '');
    try {
      const u = new URL(gw);
      // CSP allows frame-src 'self' http://localhost:3000 — the noVNC frame must
      // be served from the same origin as gatewayUrl. If the gateway runs on a
      // different port, update the frame-src in app.html.
      return `${gw}/novnc/vnc.html?autoconnect=true&host=${encodeURIComponent(u.hostname)}&port=${encodeURIComponent(u.port || '80')}`;
    } catch {
      return null;
    }
  });

  // --- Data loading ---
  async function load() {
    loading = true;
    error = null;
    denied = false;

    const [sumRes, instRes, ctxRes, tabRes] = await Promise.all([
      api.get<BrowserSummary>('/v1/browser/summary'),
      api.get<{ instances: BrowserInstance[] }>('/v1/browser/instances'),
      api.get<{ contexts: BrowserContext[] }>('/v1/browser/contexts'),
      api.get<{ tabs: BrowserTab[] }>('/v1/browser/tabs'),
    ]);

    loading = false;

    // Respect denied / error from the primary endpoint
    if (instRes.denied) { denied = true; return; }
    if (instRes.error) { error = instRes.error; return; }

    summary = sumRes.data ?? null;
    instances = instRes.data?.instances ?? [];
    contexts = ctxRes.data?.contexts ?? [];
    tabs = tabRes.data?.tabs ?? [];
  }

  // --- xterm init/cleanup ---
  async function initTerminal() {
    if (!terminalEl || termReady) return;
    try {
      const { Terminal } = await import('@xterm/xterm');
      // Dynamic CSS import — Vite will bundle this
      await import('@xterm/xterm/css/xterm.css');
      term = new Terminal({
        theme: { background: '#111418', foreground: '#d4d4d8' },
        fontFamily: '"Cascadia Mono", "Fira Code", monospace',
        fontSize: 12,
        rows: 20,
        cols: 90,
        cursorBlink: true,
      });
      (term as unknown as { open: (el: HTMLElement) => void }).open(terminalEl);
      writeWelcome();
      termReady = true;
    } catch (e) {
      console.warn('xterm init failed', e);
    }
  }

  function writeWelcome() {
    if (!term) return;
    term.writeln('\x1b[32m● UBAG Browser Session Log\x1b[0m');
    term.writeln('\x1b[90m─────────────────────────────────────────────────────────────────────────────────\x1b[0m');
    if (selectedInstance) {
      term.writeln(`\x1b[36mInstance:\x1b[0m  ${selectedInstance.id}`);
      term.writeln(`\x1b[36mStatus:\x1b[0m    ${selectedInstance.status}`);
      term.writeln(`\x1b[36mContexts:\x1b[0m  ${selectedInstance.context_count ?? 0}`);
      term.writeln('\x1b[90m─────────────────────────────────────────────────────────────────────────────────\x1b[0m');
      term.writeln('\x1b[33mℹ Live log streaming requires a direct WebSocket connection to the gateway.\x1b[0m');
      term.writeln(`\x1b[33mℹ Connect to: ${$settings.gatewayUrl}/v1/browser/instances/${selectedInstance.id}/logs\x1b[0m`);
    } else {
      term.writeln('\x1b[33mSelect an instance row to view its log stream.\x1b[0m');
    }
  }

  function refreshTerminal() {
    if (!term) return;
    (term as unknown as { clear: () => void }).clear?.();
    writeWelcome();
  }

  function selectInstance(inst: BrowserInstance) {
    selectedInstance = inst;
    // Re-draw terminal with new instance info
    if (termReady) refreshTerminal();
  }

  function truncate(s: string | undefined | null, n: number): string {
    if (!s) return '—';
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // --- Lifecycle ---
  onMount(() => {
    load();
  });

  // When terminal element becomes available, initialise xterm
  $effect(() => {
    if (terminalEl && !termReady) {
      initTerminal();
    }
  });

  // When selectedInstance changes after terminal is ready, refresh output
  $effect(() => {
    if (selectedInstance && termReady) {
      refreshTerminal();
    }
  });

  onDestroy(() => {
    term?.dispose();
  });
</script>

<div class="space-y-6">
  <!-- Header -->
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-display font-bold text-ink">Browser Sessions</h1>
      <p class="text-xs text-ink-mute mt-0.5">Live Chromium automation via UBAG gateway</p>
    </div>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  {#if loading}
    <div class="text-ink-mute text-sm animate-pulse">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="browser sessions" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    <!-- Summary cards -->
    {#if summary}
      <div class="grid grid-cols-3 gap-4">
        {#each [
          { label: 'Instances', value: summary.instances, color: 'text-marine' },
          { label: 'Contexts', value: summary.contexts, color: 'text-saffron' },
          { label: 'Tabs', value: summary.tabs, color: 'text-success' },
        ] as card}
          <div class="rounded-md border border-rule bg-paper-soft px-4 py-3 flex flex-col gap-1">
            <span class="text-xs text-ink-mute uppercase tracking-wider font-mono">{card.label}</span>
            <span class="text-2xl font-display font-bold {card.color}">{card.value}</span>
          </div>
        {/each}
      </div>
    {/if}

    <!-- Main split: instances table + viewer -->
    <div class="grid grid-cols-1 xl:grid-cols-2 gap-6">
      <!-- LEFT: Instances table -->
      <div class="space-y-3">
        <h2 class="text-sm font-semibold text-ink uppercase tracking-wider">Instances</h2>
        {#if instances.length === 0}
          <EmptyState message="No browser instances." hint="Start a browser session via the gateway." />
        {:else}
          <div class="rounded-md border border-rule overflow-x-auto">
            <table class="w-full text-sm">
              <thead class="bg-paper-soft border-b border-rule">
                <tr>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Instance ID</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Contexts</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-rule">
                {#each instances as inst (inst.id)}
                  <tr
                    role="option"
                    tabindex="0"
                    aria-label="Select instance {inst.id}"
                    aria-selected={selectedInstance?.id === inst.id}
                    class="cursor-pointer transition-colors"
                    class:bg-accent-soft={selectedInstance?.id === inst.id}
                    class:hover:bg-paper-soft={selectedInstance?.id !== inst.id}
                    onclick={() => selectInstance(inst)}
                    onkeydown={(e) => e.key === 'Enter' && selectInstance(inst)}
                  >
                    <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{inst.id.slice(0, 12)}…</td>
                    <td class="px-4 py-2.5"><StatusBadge status={inst.status} /></td>
                    <td class="px-4 py-2.5 text-ink-soft text-center">{inst.context_count ?? 0}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        <!-- Contexts list -->
        {#if contexts.length > 0}
          <h2 class="text-sm font-semibold text-ink uppercase tracking-wider pt-2">Contexts</h2>
          <div class="rounded-md border border-rule overflow-x-auto">
            <table class="w-full text-sm">
              <thead class="bg-paper-soft border-b border-rule">
                <tr>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Context ID</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Instance</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Tabs</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-rule">
                {#each contexts as ctx (ctx.id)}
                  <tr class="hover:bg-paper-soft transition-colors">
                    <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{ctx.id.slice(0, 12)}…</td>
                    <td class="px-4 py-2.5 font-mono text-xs text-ink-soft">{ctx.instance_id.slice(0, 8)}…</td>
                    <td class="px-4 py-2.5 text-ink-soft text-center">{ctx.tab_count ?? 0}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        <!-- Tabs list -->
        {#if tabs.length > 0}
          <h2 class="text-sm font-semibold text-ink uppercase tracking-wider pt-2">Tabs</h2>
          <div class="rounded-md border border-rule overflow-x-auto">
            <table class="w-full text-sm">
              <thead class="bg-paper-soft border-b border-rule">
                <tr>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Tab ID</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Context</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">URL</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Title</th>
                  <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-rule">
                {#each tabs as tab (tab.id)}
                  <tr class="hover:bg-paper-soft transition-colors">
                    <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{tab.id.slice(0, 8)}…</td>
                    <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{tab.context_id.slice(0, 8)}…</td>
                    <td class="px-4 py-2.5 text-xs text-ink-soft max-w-[12rem] truncate" title={tab.url ?? ''}>{truncate(tab.url, 40)}</td>
                    <td class="px-4 py-2.5 text-xs text-ink max-w-[10rem] truncate" title={tab.title ?? ''}>{truncate(tab.title, 30)}</td>
                    <td class="px-4 py-2.5"><StatusBadge status={tab.status ?? 'unknown'} /></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        {#if instances.length === 0 && contexts.length === 0 && tabs.length === 0}
          <EmptyState message="No browser sessions active." hint="Browser instances, contexts, and tabs will appear here." />
        {/if}
      </div>

      <!-- RIGHT: Selected instance viewer -->
      <div class="space-y-4">
        {#if selectedInstance}
          <!-- Instance header -->
          <div class="flex items-center justify-between">
            <div>
              <h2 class="text-sm font-semibold text-ink uppercase tracking-wider">Session Viewer</h2>
              <p class="text-xs font-mono text-ink-mute mt-0.5">{selectedInstance.id}</p>
            </div>
            <StatusBadge status={selectedInstance.status} />
          </div>

          <!-- xterm log pane -->
          <div class="space-y-1.5">
            <div class="flex items-center justify-between">
              <span class="text-xs font-mono text-ink-mute uppercase tracking-wider">Log Stream</span>
              <span class="text-xs text-ink-mute italic">WebSocket connection required for live data</span>
            </div>
            <div
              bind:this={terminalEl}
              class="w-full rounded-md overflow-hidden border border-rule"
              style="min-height: 180px;"
              aria-label="Terminal log pane for instance {selectedInstance.id}"
            ></div>
          </div>

          <!-- Contextual info for selected instance -->
          {#if instanceContexts.length > 0}
            <div class="rounded-md border border-rule bg-paper-soft px-4 py-3 text-xs space-y-1">
              <p class="font-mono text-ink-mute uppercase tracking-wider mb-2">Instance Contexts</p>
              {#each instanceContexts as ctx}
                <div class="flex items-center gap-4">
                  <span class="font-mono text-ink">{ctx.id.slice(0, 12)}…</span>
                  <span class="text-ink-mute">{ctx.tab_count ?? 0} tab{ctx.tab_count !== 1 ? 's' : ''}</span>
                </div>
              {/each}
            </div>
          {/if}

          {#if instanceTabs.length > 0}
            <div class="rounded-md border border-rule bg-paper-soft px-4 py-3 text-xs space-y-1">
              <p class="font-mono text-ink-mute uppercase tracking-wider mb-2">Instance Tabs</p>
              {#each instanceTabs as tab}
                <div class="flex items-center gap-3">
                  <span class="font-mono text-ink-mute">{tab.id.slice(0, 8)}…</span>
                  <span class="text-ink truncate max-w-[20rem]" title={tab.url ?? ''}>{truncate(tab.url, 50)}</span>
                  <StatusBadge status={tab.status ?? 'unknown'} />
                </div>
              {/each}
            </div>
          {/if}

          <!-- noVNC viewer -->
          <div class="space-y-1.5">
            <div class="flex items-center justify-between">
              <span class="text-xs font-mono text-ink-mute uppercase tracking-wider">noVNC Viewer</span>
              <span class="text-xs text-ink-mute italic">Requires noVNC served by gateway</span>
            </div>
            {#if noVncSrc}
              <!-- CSP note: frame-src in app.html allows 'self' and http://localhost:3000.
                   If the gateway is on a different host/port, update frame-src in app.html. -->
              <iframe
                src={noVncSrc}
                class="w-full rounded-md border border-rule bg-paper-warm"
                style="height: 20rem;"
                title="noVNC Viewer — {selectedInstance.id}"
                sandbox="allow-same-origin allow-scripts allow-forms"
                aria-label="noVNC remote desktop viewer for instance {selectedInstance.id}"
              ></iframe>
            {:else}
              <div class="flex items-center justify-center rounded-md border border-rule bg-paper-warm" style="height: 20rem;">
                <div class="text-center text-xs text-ink-mute space-y-1">
                  <p class="font-medium text-ink">noVNC unavailable</p>
                  <p>Could not parse gateway URL: <span class="font-mono">{$settings.gatewayUrl}</span></p>
                  <p>Update Settings to point to a valid gateway.</p>
                </div>
              </div>
            {/if}
          </div>
        {:else}
          <!-- No instance selected -->
          <div class="flex items-center justify-center rounded-md border border-rule border-dashed bg-paper-warm" style="min-height: 28rem;">
            <div class="text-center text-sm text-ink-mute space-y-2 px-8">
              <div class="w-12 h-12 mx-auto rounded-md bg-paper-soft border border-rule flex items-center justify-center" aria-hidden="true">
                <svg class="w-6 h-6 text-ink-mute" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z" />
                </svg>
              </div>
              <p class="font-medium text-ink">No instance selected</p>
              <p>Click a row in the Instances table to open the xterm log pane and noVNC viewer.</p>
            </div>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>
