<script lang="ts">
  import { onMount, onDestroy } from 'svelte';

  // The live-browser bridge (tools/live-browser/bridge.mjs) streams the real
  // Chrome as JPEG frames over this WebSocket and accepts mouse/keyboard input
  // back. localStorage override lets an operator point at a non-default bridge.
  function defaultWsUrl(): string {
    if (typeof localStorage !== 'undefined') {
      const stored = localStorage.getItem('ubag_live_browser_ws');
      if (stored) return stored;
    }
    return 'ws://127.0.0.1:58090';
  }

  let { wsUrl = defaultWsUrl() }: { wsUrl?: string } = $props();

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
      ws = new WebSocket(wsUrl);
    } catch {
      connecting = false;
      scheduleRetry();
      return;
    }
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      connecting = false;
      connected = true;
      send({ t: 'targets' });
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
        if (m.type === 'meta') {
          if (m.deviceWidth) deviceW = m.deviceWidth as number;
          if (m.deviceHeight) deviceH = m.deviceHeight as number;
          if (m.url) { currentUrl = m.url as string; if (document.activeElement !== urlEl) urlInput = m.url as string; }
          if (m.targetId) currentTargetId = m.targetId as string;
        } else if (m.type === 'targets') {
          targets = (m.targets as typeof targets) ?? [];
          if (m.current) currentTargetId = m.current as string;
        }
        return;
      }
      // Binary JPEG frame.
      try {
        const blob = new Blob([ev.data as ArrayBuffer], { type: 'image/jpeg' });
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
    };
  }

  function scheduleRetry() {
    if (retryTimer) return;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      connect();
    }, 1500);
  }

  function send(obj: Record<string, unknown>) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
  }

  function frac(e: MouseEvent | WheelEvent): { fx: number; fy: number } | null {
    if (!canvas) return null;
    const r = canvas.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return null;
    const fx = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    const fy = Math.min(1, Math.max(0, (e.clientY - r.top) / r.height));
    return { fx, fy };
  }

  function onMouseDown(e: MouseEvent) {
    if (!interactive) return;
    canvas?.focus();
    const p = frac(e);
    if (p) send({ t: 'mouse', kind: 'down', fx: p.fx, fy: p.fy, button: e.button });
  }
  function onMouseUp(e: MouseEvent) {
    if (!interactive) return;
    const p = frac(e);
    if (p) send({ t: 'mouse', kind: 'up', fx: p.fx, fy: p.fy, button: e.button });
  }
  function onMouseMove(e: MouseEvent) {
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
    setTimeout(() => send({ t: 'targets' }), 800);
  }
  function switchTarget(id: string) {
    if (id && id !== currentTargetId) send({ t: 'attach', targetId: id });
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
    { label: 'Claude', url: 'https://claude.ai' },
    { label: 'Gemini', url: 'https://gemini.google.com' },
    { label: 'DeepSeek', url: 'https://chat.deepseek.com' },
  ];

  onMount(() => {
    if (canvas) ctx = canvas.getContext('2d');
    connect();
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

    {#if targets.length > 1}
      <select
        class="px-2 py-1 rounded border border-rule bg-paper text-ink text-xs max-w-[10rem]"
        value={currentTargetId}
        onchange={(e) => switchTarget((e.target as HTMLSelectElement).value)}
        aria-label="Switch tab"
      >
        {#each targets as t}
          <option value={t.id}>{t.title || t.url}</option>
        {/each}
      </select>
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
  <div class="relative bg-[#0b0d10]">
    {#if !connected}
      <div class="flex items-center justify-center" style="min-height: 24rem;">
        <div class="text-center text-xs text-ink-mute space-y-2 px-8 max-w-md">
          <p class="font-medium text-ink text-sm">Live browser bridge not connected</p>
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
      style="aspect-ratio: {deviceW} / {deviceH};"
      onmousedown={onMouseDown}
      onmouseup={onMouseUp}
      onmousemove={onMouseMove}
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
