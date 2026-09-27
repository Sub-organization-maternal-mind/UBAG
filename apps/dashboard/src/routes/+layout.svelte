<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { base } from '$app/paths';
  import { api } from '$lib/api/client';
  import { settings } from '$lib/stores/settings';
  import { pollWhileVisible } from '$lib/poll';
  import type { HealthResponse } from '$lib/api/types';
  import type { Snippet } from 'svelte';
  import {
    LayoutDashboard, Briefcase, Target, Puzzle, AppWindow, Smartphone,
    AlertTriangle, Globe, Webhook, FileText, GitBranch, Database,
    Shield, Users, CreditCard, Settings2, BarChart3, MessagesSquare, Menu, X,
    KeyRound, ServerCog, Sparkles
  } from 'lucide-svelte';

  let { children }: { children: Snippet } = $props();

  // Nav grouped by the documented IA (ux.md): Overview → Ops → Config → Org/Admin → Observability.
  // The nav must contain exactly one <a> per dashboard route (Playwright nav contract).
  const navGroups: { label: string; items: { href: string; label: string; icon: typeof Briefcase }[] }[] = [
    {
      label: 'Overview',
      items: [
        { href: '/', label: 'Overview', icon: LayoutDashboard },
      ],
    },
    {
      label: 'Operations',
      items: [
        { href: '/jobs', label: 'Jobs', icon: Briefcase },
        { href: '/targets', label: 'Targets', icon: Target },
        { href: '/adapters', label: 'Adapters', icon: Puzzle },
        { href: '/apps', label: 'Apps', icon: AppWindow },
        { href: '/devices', label: 'Devices', icon: Smartphone },
        { href: '/antigravity', label: 'Antigravity', icon: Sparkles },
        { href: '/failed', label: 'Failed/DLQ', icon: AlertTriangle },
        { href: '/browser', label: 'Browser Sessions', icon: Globe },
        { href: '/conversations', label: 'Conversations', icon: MessagesSquare },
      ],
    },
    {
      label: 'Configuration',
      items: [
        { href: '/webhooks', label: 'Webhooks', icon: Webhook },
        { href: '/templates', label: 'Templates', icon: FileText },
        { href: '/workflows', label: 'Workflows', icon: GitBranch },
        { href: '/cache', label: 'Cache', icon: Database },
        { href: '/settings', label: 'Settings', icon: Settings2 },
      ],
    },
    {
      label: 'Organization & Admin',
      items: [
        { href: '/audit', label: 'Audit', icon: Shield },
        { href: '/users', label: 'Users & Roles', icon: Users },
        { href: '/security', label: 'Security', icon: KeyRound },
        { href: '/admin', label: 'Administration', icon: ServerCog },
        { href: '/quotas', label: 'Quotas & Billing', icon: CreditCard },
      ],
    },
    {
      label: 'Observability',
      items: [
        { href: '/metrics', label: 'Metrics', icon: BarChart3 },
      ],
    },
  ];

  // Health polling state
  let health = $state<HealthResponse | null>(null);
  let healthError = $state(false);
  let sidebarOpen = $state(false);

  async function checkHealth() {
    const res = await api.get<HealthResponse>('/v1/health');
    if (res.status === 200 && res.data) {
      health = res.data;
      healthError = false;
    } else {
      healthError = true;
    }
  }

  // Escape closes the mobile drawer while it is open.
  $effect(() => {
    if (!sidebarOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') sidebarOpen = false;
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  onMount(() => {
    // Health check pauses while the tab is hidden, catches up on re-visible.
    const stopPolling = pollWhileVisible(checkHealth, 30_000);
    // Register service worker (path respects the deployment base)
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register(`${base}/sw.js`).catch(() => {});
    }
    return stopPolling;
  });

  // Jump back to the top of the content pane on navigation.
  let mainEl: HTMLElement | undefined = $state();
  let lastPath = $state('');
  $effect(() => {
    const path = $page.url.pathname;
    if (lastPath && path !== lastPath && mainEl) mainEl.scrollTo({ top: 0 });
    lastPath = path;
  });
</script>

<!-- Skip link for keyboard/a11y -->
<a class="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:p-4 focus:bg-paper focus:text-ink" href="#main-content">
  Skip to main content
</a>

<div class="flex h-dvh overflow-hidden bg-paper text-ink">
  <!-- Sidebar overlay on mobile -->
  {#if sidebarOpen}
    <button
      class="ubag-fade-in fixed inset-0 z-30 bg-ink/40 lg:hidden"
      onclick={() => (sidebarOpen = false)}
      aria-label="Close navigation"
    ></button>
  {/if}

  <!-- Sidebar nav -->
  <aside
    class="fixed inset-y-0 left-0 z-40 w-64 flex flex-col bg-paper-soft border-r border-rule transform transition-transform duration-200 ease-out lg:relative lg:translate-x-0"
    class:translate-x-0={sidebarOpen}
    class:-translate-x-full={!sidebarOpen}
    aria-label="Navigation"
  >
    <!-- Brand -->
    <div class="flex items-center gap-3 px-5 py-4 border-b border-rule">
      <span class="font-display font-bold text-lg text-accent-deep">UBAG</span>
      <span class="text-xs font-mono text-ink-mute uppercase tracking-widest">Operator</span>
    </div>

    <!-- Nav list -->
    <nav class="flex-1 overflow-y-auto py-3" aria-label="Dashboard sections">
      {#each navGroups as group}
        <div class="px-4 pb-1 pt-4 first:pt-1">
          <span
            class="text-[0.6875rem] font-semibold uppercase tracking-widest text-ink-mute"
            aria-hidden="true">{group.label}</span
          >
        </div>
        <ul class="space-y-0.5 px-2">
          {#each group.items as item}
            {@const fullHref = base + item.href}
            {@const currentPath = $page.url.pathname}
            {@const isActive = currentPath === fullHref || (item.href !== '/' && currentPath.startsWith(fullHref))}
            <li>
              <a
                href={fullHref}
                class="flex items-center gap-3 rounded-md px-3 py-2.5 text-sm transition-colors duration-100"
                class:bg-accent-soft={isActive}
                class:text-accent-deep={isActive}
                class:font-medium={isActive}
                class:text-ink-soft={!isActive}
                class:hover:bg-rule-soft={!isActive}
                aria-current={isActive ? 'page' : undefined}
                onclick={() => { sidebarOpen = false; }}
              >
                <item.icon class="h-4 w-4 shrink-0" aria-hidden="true" />
                {item.label}
              </a>
            </li>
          {/each}
        </ul>
      {/each}
    </nav>

    <!-- Sidebar footer: gateway URL -->
    <div class="border-t border-rule px-4 py-3 text-xs font-mono text-ink-mute">
      <p class="truncate" title={$settings.gatewayUrl}>{$settings.gatewayUrl}</p>
    </div>
  </aside>

  <!-- Main content area -->
  <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
    <!-- Top bar -->
    <header class="flex shrink-0 items-center gap-3 border-b border-rule bg-paper-soft px-4 py-2.5">
      <!-- Mobile menu toggle -->
      <button
        class="flex h-10 w-10 items-center justify-center rounded-md text-ink-soft transition-colors hover:bg-rule-soft hover:text-ink lg:hidden"
        onclick={() => (sidebarOpen = !sidebarOpen)}
        aria-label={sidebarOpen ? 'Close navigation' : 'Open navigation'}
        aria-expanded={sidebarOpen}
      >
        {#if sidebarOpen}
          <X class="h-5 w-5" aria-hidden="true" />
        {:else}
          <Menu class="h-5 w-5" aria-hidden="true" />
        {/if}
      </button>

      <!-- Health indicator -->
      <div class="flex items-center gap-2 font-mono text-xs" aria-live="polite" aria-label="Connection status">
        <span
          class="h-2 w-2 shrink-0 rounded-full"
          class:bg-success={!healthError && health !== null}
          class:bg-danger={healthError}
          class:bg-ink-mute={!healthError && health === null}
          aria-hidden="true"
        ></span>
        {#if healthError}
          <span class="text-danger">Disconnected</span>
        {:else if health}
          <span class="text-ink-soft">Connected</span>
        {:else}
          <span class="text-ink-mute">Connecting…</span>
        {/if}
      </div>

      <div class="flex-1"></div>
    </header>

    <!-- Page content -->
    <main id="main-content" class="flex-1 overflow-y-auto p-4 sm:p-6" tabindex="-1" bind:this={mainEl}>
      <div class="mx-auto w-full max-w-[1440px]">
        {@render children()}
      </div>
    </main>
  </div>
</div>
