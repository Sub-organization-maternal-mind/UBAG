<script module lang="ts">
  // Build-time default (see vite.config.ts `define`, same pattern as
  // settings.ts): lets a local deployment bake in a tunnelled remote bridge —
  // e.g. the production VPS Chrome reached over an SSH tunnel — so every
  // operator browser profile works with zero manual setup.
  //
  // This lives in a module script because `declare` is only legal at module
  // scope; svelte-check rejects a modifier-bearing declare inside the instance
  // script. The symbol is still replaced textually by Vite's `define`, and the
  // `typeof` guard in defaultWsUrl() handles an undefined injection.
  declare const __UBAG_DEFAULT_LIVE_BROWSER_WS__: string | undefined;
</script>

<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { RefreshCw } from 'lucide-svelte';

  // The live-browser bridge (tools/live-browser/bridge.mjs) streams the real
  // Chrome as JPEG frames over this WebSocket and accepts mouse/keyboard input
  // back. localStorage override lets an operator point at a non-default bridge.
  function defaultWsUrl(): string {
    if (typeof localStorage !== 'undefined') {
      const stored = localStorage.getItem('ubag_live_browser_ws');
      if (stored) return stored;
    }
    if (
      typeof __UBAG_DEFAULT_LIVE_BROWSER_WS__ !== 'undefined' &&
      __UBAG_DEFAULT_LIVE_BROWSER_WS__
    ) {
      return __UBAG_DEFAULT_LIVE_BROWSER_WS__;
    }
    // Served from a real host (VPS deployment): the bridge runs in a container
    // behind nginx at /live-ws on the same origin. Use wss/ws to match the page
    // scheme (browsers block ws:// from an https page except to localhost).
    if (
      typeof location !== 'undefined' &&
      !['localhost', '127.0.0.1', '[::1]', ''].includes(location.hostname)
    ) {
      const scheme = location.protocol === 'https:' ? 'wss://' : 'ws://';
      return scheme + location.host + '/live-ws';
    }
    // Local single-machine dev: bridge runs on the loopback default port.
    return 'ws://127.0.0.1:58090';
  }

  let { wsUrl = defaultWsUrl() }: { wsUrl?: string } = $props();

  // An override saved in this browser (an earlier deployment's setup hint)
  // beats every default above — a stale one points at a port no bridge
  // listens on and the panel shows Offline with no visible reason. Track the
  // source so the offline panel can show exactly what is being dialed and
  // offer a one-click reset back to the built-in default.
  let savedWsUrl = $state<string | null>(null);
  try {
    savedWsUrl =
      typeof localStorage !== 'undefined'
        ? localStorage.getItem('ubag_live_browser_ws')
        : null;
  } catch {
    savedWsUrl = null;
  }
  let activeWsUrl = $state(savedWsUrl || wsUrl);

  function resetBridgeUrl() {
    try { localStorage.removeItem('ubag_live_browser_ws'); } catch { /* ignore */ }
    savedWsUrl = null;
    activeWsUrl = defaultWsUrl();
    reconnect();
  }

  let canvas = $state<HTMLCanvasElement | null>(null);
  let ctx: CanvasRenderingContext2D | null = null;
  let ws: WebSocket | null = null;

  let connected = $state(false);
  let connecting = $state(false);
  let interactive = $state(true);
  let currentUrl = $state('');
  let urlInput = $state('');
  let targets = $state<{ id: string; title: string; url: string }[]>([]);
  let currentTargetId = $state('');
  let hasFrame = $state(false);
  let capturing = $state(false);
  // True from a reset request until the bridge pushes the tab list of the
  // relaunched browser.
  let resetting = $state(false);
  let resetDialog = $state<HTMLDialogElement | null>(null);

  // Live-stream viewer registry (from the bridge): the admin can see every
  // dashboard tab streaming the production Chrome and terminate any of them —
  // each forgotten background tab keeps the screencast hot on the VPS.
  let clients = $state<{ id: string; since: number; hidden: boolean; alive: boolean }[]>([]);
  let myClientId = $state('');
  let showClients = $state(false);
  let showTabs = $state(false);

  function closeTab(targetId: string) {
    send({ t: 'closetab', targetId });
    // The bridge echoes a fresh targets list; poll once as a safety net.
    setTimeout(() => send({ t: 'targets' }), 600);
  }

  let deviceW = $state(1280);
  let deviceH = $state(720);
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let lastMoveSent = 0;
  let manualClose = false;

  const SPECIAL: Record<string, number> = {
    Enter: 13, Backspace: 8, Tab: 9, Escape: 27, Delete: 46,
    ArrowUp: 38, ArrowDown: 40, ArrowLeft: 37, ArrowRight: 39,
    Home: 36, End: 35, PageUp: 33, PageDown: 34,
  };

  function connect() {
    if (connecting || (ws && ws.readyState === WebSocket.OPEN)) return;
    connecting = true;
    manualClose = false;
    try {
      ws = new WebSocket(activeWsUrl);
    } catch {
      connecting = false;
      scheduleRetry();
      return;
    }
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      connecting = false;
      connected = true;
      retryDelay = RETRY_BASE; // successful connection resets backoff
      send({ t: 'targets' });
      // Register our real visibility so a backgrounded dashboard tab stops
      // receiving frames (and stops holding the VPS screencast hot).
      send({ t: 'visibility', hidden: document.hidden });
    };

    ws.onclose = () => {
      connected = false;
      connecting = false;
      if (!manualClose) scheduleRetry();
    };

    ws.onerror = () => {
      // onclose will follow and handle retry.
    };

    ws.onmessage = async (ev) => {
      if (typeof ev.data === 'string') {
        let m: Record<string, unknown>;
        try { m = JSON.parse(ev.data); } catch { return; }
        if (m.type === 'clients') {
          myClientId = String(m.self ?? '');
          clients = (m.clients as typeof clients) ?? [];
        } else if (m.type === 'meta') {
          if (m.deviceWidth) deviceW = m.deviceWidth as number;
          if (m.deviceHeight) deviceH = m.deviceHeight as number;
          if (m.url) { currentUrl = m.url as string; if (document.activeElement !== urlEl) urlInput = m.url as string; }
          if (m.targetId) currentTargetId = m.targetId as string;
        } else if (m.type === 'targets') {
          targets = (m.targets as typeof targets) ?? [];
          if (m.current) currentTargetId = m.current as string;
          resetting = false;
        }
        return;
      }
      // Binary JPEG frame. Decodes are serialized and only the latest pending
      // frame is kept, so a fast bridge can't pile up bitmaps.
      void decodeFrame(ev.data as ArrayBuffer);
    };
  }

  let pendingFrame: ArrayBuffer | null = null;
  let decoding = false;

  async function decodeFrame(data: ArrayBuffer) {
    if (decoding) {
      pendingFrame = data;
      return;
    }
    decoding = true;
    try {
      const blob = new Blob([data], { type: 'image/jpeg' });
      const bmp = await createImageBitmap(blob);
      if (canvas) {
        if (canvas.width !== bmp.width || canvas.height !== bmp.height) {
          canvas.width = bmp.width;
          canvas.height = bmp.height;
        }
        ctx?.drawImage(bmp, 0, 0);
        hasFrame = true;
      }
      bmp.close();
    } catch {
      /* ignore a bad frame */
    }
    decoding = false;
    if (pendingFrame) {
      const next = pendingFrame;
      pendingFrame = null;
      void decodeFrame(next);
    }
  }

  const RETRY_BASE = 1500;
  const RETRY_MAX = 30_000;
  let retryDelay = RETRY_BASE;

  function scheduleRetry() {
    if (retryTimer) return;
    // Hidden tab: don't burn reconnect attempts; the visibility handler retries.
    if (document.hidden) return;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      connect();
    }, retryDelay);
    // Exponential backoff, capped — a bridge that is down for hours must not
    // hot-loop reconnect attempts every 1.5s forever.
    retryDelay = Math.min(RETRY_MAX, retryDelay * 2);
  }

  function send(obj: Record<string, unknown>) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
  }

  function frac(e: MouseEvent | WheelEvent | PointerEvent): { fx: number; fy: number } | null {
    if (!canvas) return null;
    const r = canvas.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return null;
    const fx = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    const fy = Math.min(1, Math.max(0, (e.clientY - r.top) / r.height));
    return { fx, fy };
  }

  // Pointer events cover mouse, touch and pen in one path — the remote page
  // gets down/up/move pairs from touch drags too. touch-action: none on the
  // canvas keeps the browser from scrolling during a drag.
  function onPointerDown(e: PointerEvent) {
    if (!interactive) return;
    canvas?.focus();
    try { canvas?.setPointerCapture(e.pointerId); } catch { /* ignore */ }
    const p = frac(e);
    if (p) send({ t: 'mouse', kind: 'down', fx: p.fx, fy: p.fy, button: e.button });
  }
  function onPointerUp(e: PointerEvent) {
    if (!interactive) return;
    try { canvas?.releasePointerCapture(e.pointerId); } catch { /* ignore */ }
    const p = frac(e);
    if (p) send({ t: 'mouse', kind: 'up', fx: p.fx, fy: p.fy, button: e.button });
  }
  function onPointerMove(e: PointerEvent) {
    if (!interactive) return;
    const now = Date.now();
    if (now - lastMoveSent < 33) return; // ~30fps input cap
    lastMoveSent = now;
    const p = frac(e);
    if (p) send({ t: 'mouse', kind: 'move', fx: p.fx, fy: p.fy });
  }
  function onWheel(e: WheelEvent) {
    if (!interactive) return;
    e.preventDefault();
    const p = frac(e);
    if (p) send({ t: 'mouse', kind: 'wheel', fx: p.fx, fy: p.fy, deltaY: e.deltaY });
  }
  function onContextMenu(e: MouseEvent) {
    // Forward right-clicks to the remote page instead of the local menu.
    if (interactive) e.preventDefault();
  }

  function onKeyDown(e: KeyboardEvent) {
    if (!interactive) return;
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      send({ t: 'text', text: e.key });
      e.preventDefault();
      return;
    }
    if (e.key in SPECIAL) {
      send({ t: 'key', kind: 'down', info: { key: e.key, code: e.code, keyCode: SPECIAL[e.key] } });
      e.preventDefault();
    }
  }
  function onKeyUp(e: KeyboardEvent) {
    if (!interactive) return;
    if (e.key in SPECIAL) {
      send({ t: 'key', kind: 'up', info: { key: e.key, code: e.code, keyCode: SPECIAL[e.key] } });
      e.preventDefault();
    }
  }

  let urlEl: HTMLInputElement | null = $state(null);

  function go() {
    let u = urlInput.trim();
    if (!u) return;
    if (!/^https?:\/\//i.test(u)) u = 'https://' + u;
    send({ t: 'navigate', url: u });
  }
  function newTab() {
    let u = urlInput.trim();
    if (u && !/^https?:\/\//i.test(u)) u = 'https://' + u;
    send({ t: 'newtab', url: u });
  }
  function switchTarget(id: string) {
    if (id && id !== currentTargetId) send({ t: 'attach', targetId: id });
    showTabs = false;
  }
  function resetBrowser() {
    resetDialog?.close();
    resetting = true;
    send({ t: 'reset' });
  }
  function reconnect() {
    manualClose = true;
    try { ws?.close(); } catch { /* ignore */ }
    ws = null;
    connected = false;
    setTimeout(connect, 150);
  }

  const providerShortcuts = [
    { label: 'ChatGPT', url: 'https://chatgpt.com' },
    { label: 'Gemini', url: 'https://gemini.google.com' },
    { label: 'DeepSeek', url: 'https://chat.deepseek.com' },
    { label: 'DuckAI', url: 'https://duck.ai' },
  ];

  onMount(() => {
    if (canvas) ctx = canvas.getContext('2d');
    connect();
    // Keep the bridge informed when this tab is backgrounded/restored.
    const onVisibility = () => {
      send({ t: 'visibility', hidden: document.hidden });
      if (!document.hidden && !manualClose && !connected && !connecting) scheduleRetry();
    };
    document.addEventListener('visibilitychange', onVisibility);
    return () => document.removeEventListener('visibilitychange', onVisibility);
  });

  onDestroy(() => {
    manualClose = true;
    if (retryTimer) clearTimeout(retryTimer);
    try { ws?.close(); } catch { /* ignore */ }
  });
</script>

<div class="rounded-md border border-rule bg-paper-soft">
  <!-- Toolbar -->
  <div class="flex flex-wrap items-center gap-2 px-3 py-2 border-b border-rule">
    <div class="flex items-center gap-1.5 shrink-0" aria-live="polite">
      <span
        class="w-2 h-2 rounded-full"
        class:bg-success={connected}
        class:bg-danger={!connected && !connecting}
        class:bg-ink-mute={connecting}
      ></span>
      <span class="text-xs font-mono text-ink-mute">{connected ? 'Live' : connecting ? 'Connecting' : 'Offline'}</span>
    </div>

    <form class="flex-1 flex items-center gap-1.5 min-w-[12rem]" onsubmit={(e) => { e.preventDefault(); go(); }}>
      <input
        bind:this={urlEl}
        bind:value={urlInput}
        type="text"
        placeholder="https://chatgpt.com"
        class="flex-1 px-2 py-1 rounded border border-rule bg-paper text-ink text-xs font-mono focus:outline-none focus:border-accent"
        aria-label="Live browser address"
      />
      <button type="submit" class="px-2.5 py-1 rounded bg-accent text-paper-soft text-xs font-medium hover:bg-accent-deep transition-colors">Go</button>
      <button type="button" onclick={newTab} class="px-2.5 py-1 rounded border border-rule text-ink text-xs hover:bg-rule-soft transition-colors">+ Tab</button>
    </form>

<!-- Open tabs in the remote Chrome: view + terminate (resource control) -->
    {#if connected && targets.length > 0}
      <div class="relative shrink-0">
        <button
          type="button"
          onclick={() => (showTabs = !showTabs)}
          class="px-2.5 py-1 rounded border border-rule text-ink text-xs hover:bg-rule-soft transition-colors"
          aria-label="Browser tabs"
        >
          tabs {targets.length}
        </button>
        {#if showTabs}
          <div class="absolute right-0 top-full mt-1 w-72 rounded border border-rule bg-paper-soft shadow-md z-10 p-2">
            <p class="text-[10px] font-mono text-ink-mute px-1 pb-1">Open tabs in the remote Chrome</p>
            {#each targets as t (t.id)}
              <div class="flex items-center gap-1.5 px-1 py-1 rounded hover:bg-rule-soft">
                <button
                  type="button"
                  class="flex-1 min-w-0 text-left truncate text-xs {t.id === currentTargetId ? 'text-accent font-medium' : 'text-ink'}"
                  title={t.url}
                  onclick={() => { switchTarget(t.id); showTabs = false; }}
                >{t.title || t.url || 'untitled'}</button>
                <button
                  type="button"
                  onclick={() => closeTab(t.id)}
                  class="px-1.5 py-0.5 rounded border border-rule text-[10px] text-danger hover:bg-danger-soft transition-colors shrink-0"
                  aria-label="Terminate tab"
                >Terminate</button>
              </div>
            {:else}
              <p class="text-xs text-ink-mute px-1 py-1">No open tabs.</p>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- Viewer registry: admin visibility + terminate (resource control) -->
    {#if connected}
      <div class="relative shrink-0">
        <button
          type="button"
          onclick={() => (showClients = !showClients)}
          class="px-2.5 py-1 rounded border border-rule text-ink text-xs hover:bg-rule-soft transition-colors"
          aria-label="Live stream viewers"
        >
          viewers {clients.length}
        </button>
        {#if showClients}
          <div class="absolute right-0 top-full mt-1 w-72 rounded border border-rule bg-paper-soft shadow-md z-10 p-2">
            <p class="text-[10px] font-mono text-ink-mute px-1 pb-1">Live stream viewers</p>
            {#each clients as c (c.id)}
              <div class="flex items-center gap-1.5 px-1 py-1 rounded hover:bg-rule-soft">
                <span class="font-mono text-xs text-ink">#{c.id}</span>
                {#if c.id === myClientId}<span class="text-[10px] text-ink-mute">(you)</span>{/if}
                {#if c.hidden}
                  <span class="text-[10px] text-ink-mute">hidden</span>
                {:else}
                  <span class="text-[10px] text-success">streaming</span>
                {/if}
                <span class="text-[10px] text-ink-mute ml-auto">{Math.max(1, Math.round((Date.now() - c.since) / 60000))}m</span>
                <button
                  type="button"
                  onclick={() => send({ t: 'kick', id: c.id })}
                  class="px-1.5 py-0.5 rounded border border-rule text-[10px] text-danger hover:bg-danger-soft transition-colors"
                >Terminate</button>
              </div>
            {:else}
              <p class="text-xs text-ink-mute px-1 py-1">No viewers connected.</p>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Reset: relaunch the remote Chrome. Keeps the operator logins warm. -->
      <button
        type="button"
        onclick={() => resetDialog?.showModal()}
        disabled={resetting}
        class="flex items-center gap-1 shrink-0 px-2 py-1 rounded border border-danger/40 bg-danger-soft text-danger text-xs hover:bg-danger/10 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        title="Restart the server browser: closes every tab and kills all its processes. Logins are kept."
      ><RefreshCw size={12} class={resetting ? 'animate-spin' : ''} /> {resetting ? 'Restarting…' : 'Reset browser'}</button>
    {/if}

    <label class="flex items-center gap-1.5 text-xs text-ink-soft cursor-pointer select-none shrink-0">
      <input type="checkbox" bind:checked={interactive} class="accent-accent" />
      Interactive
    </label>
  </div>

  <!-- Provider quick-links -->
  <div class="flex flex-wrap items-center gap-1.5 px-3 py-1.5 border-b border-rule bg-paper-warm">
    <span class="text-xs text-ink-mute font-mono">Log in to:</span>
    {#each providerShortcuts as p}
      <button
        onclick={() => { urlInput = p.url; send({ t: 'navigate', url: p.url }); }}
        class="px-2 py-0.5 rounded border border-rule text-xs text-ink-soft hover:bg-accent-soft hover:text-accent-deep transition-colors"
      >{p.label}</button>
    {/each}
    <span class="flex-1"></span>
    {#if currentUrl}
      <span class="text-xs font-mono text-ink-mute truncate max-w-[18rem]" title={currentUrl}>{currentUrl}</span>
    {/if}
  </div>

  <!-- Viewport -->
  <div class="relative bg-ink">
    {#if !connected}
      <div class="flex items-center justify-center min-h-[24rem]">
        <div class="text-center text-xs text-ink-mute space-y-2 px-8 max-w-md">
          <p class="font-medium text-ink text-sm">Live browser bridge not connected</p>
          <p>Trying <span class="font-mono text-ink-soft break-all">{activeWsUrl}</span>{savedWsUrl ? ' — saved in this browser' : ''}.</p>
          {#if savedWsUrl}
            <p>That bridge URL was saved here earlier and may be stale; reset it to use the built-in default.</p>
            <button onclick={resetBridgeUrl} class="px-3 py-1 rounded bg-accent text-paper-soft text-xs font-medium hover:bg-accent-deep transition-colors">Reset saved bridge URL</button>
          {/if}
          <p>Start it with the desktop launcher, or run:</p>
          <code class="block bg-paper-soft border border-rule rounded px-3 py-2 text-ink-soft font-mono text-[11px] break-all">node tools/live-browser/bridge.mjs</code>
          <p class="text-ink-mute">It launches a real Chrome with a persistent profile so your provider logins are remembered.</p>
          <button onclick={reconnect} class="mt-1 px-3 py-1 rounded border border-rule text-ink text-xs hover:bg-rule-soft transition-colors">Retry now</button>
        </div>
      </div>
    {/if}

    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <canvas
      bind:this={canvas}
      tabindex="0"
      class="block w-full h-auto outline-none"
      class:hidden={!connected}
      class:cursor-not-allowed={!interactive}
      style="aspect-ratio: {deviceW} / {deviceH}; touch-action: none;"
      onpointerdown={onPointerDown}
      onpointerup={onPointerUp}
      onpointermove={onPointerMove}
      onwheel={onWheel}
      oncontextmenu={onContextMenu}
      onkeydown={onKeyDown}
      onkeyup={onKeyUp}
      onfocus={() => (capturing = true)}
      onblur={() => (capturing = false)}
      aria-label="Live browser viewport"
    ></canvas>

    {#if connected && !hasFrame}
      <div class="absolute inset-0 flex items-center justify-center text-ink-mute text-xs animate-pulse">Waiting for first frame...</div>
    {/if}

    {#if connected && resetting}
      <div class="absolute inset-0 flex items-center justify-center bg-[#0b0d10]/70" role="status">
        <div class="text-center text-xs text-paper-soft space-y-1 px-6">
          <p class="font-medium text-sm">Restarting browser…</p>
          <p class="text-paper-soft/70">All tabs and browser processes are being discarded. Logins are kept.</p>
        </div>
      </div>
    {/if}

    {#if connected}
      <div class="absolute top-2 right-2 flex items-center gap-2">
        {#if interactive}
          <span
            class="px-2 py-0.5 rounded text-[10px] font-mono {capturing ? 'bg-success-soft text-success' : 'bg-paper-soft/80 text-ink-mute'}"
          >{capturing ? 'capturing input' : 'click to control'}</span>
        {:else}
          <span class="px-2 py-0.5 rounded text-[10px] font-mono bg-paper-soft/80 text-ink-mute">view only</span>
        {/if}
      </div>
    {/if}
  </div>
</div>

<dialog
  bind:this={resetDialog}
  class="w-full max-w-md rounded-lg border border-rule bg-paper shadow-2xl p-0 backdrop:bg-ink/40"
  aria-label="Confirm browser reset"
>
  <div class="px-5 py-4 border-b border-rule bg-paper-soft">
    <h2 class="text-lg font-display font-semibold text-ink">Reset browser?</h2>
  </div>
  <div class="p-5 space-y-2 text-sm text-ink-soft">
    <p>Chrome on the server quits and is relaunched fresh: every tab, renderer and stalled process is discarded and its memory released.</p>
    <p>Provider logins are kept (the profile is persistent). Any job running right now will fail.</p>
  </div>
  <div class="px-5 py-3 border-t border-rule flex justify-end gap-3">
    <button
      type="button"
      onclick={() => resetDialog?.close()}
      class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-paper-warm transition-colors"
    >Cancel</button>
    <button
      type="button"
      onclick={resetBrowser}
      class="px-4 py-2 rounded-md border border-danger/40 bg-danger-soft text-danger text-sm font-medium hover:bg-danger/10 transition-colors"
    >Reset browser</button>
  </div>
</dialog>
