<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { api } from '$lib/api/client';
  import { settings } from '$lib/stores/settings';
  import type { HealthResponse } from '$lib/api/types';
  import type { Snippet } from 'svelte';
  import {
    LayoutDashboard, Briefcase, Target, Puzzle, AppWindow, Smartphone,
    AlertTriangle, Globe, Webhook, FileText, GitBranch, Database,
    Shield, Users, CreditCard, Settings2, BarChart3, Sun, Moon, Menu, X
  } from 'lucide-svelte';

  let { children }: { children: Snippet } = $props();

  // Nav items matching §24.2
  const navItems = [
    { href: '/', label: 'Overview', icon: LayoutDashboard },
    { href: '/jobs', label: 'Jobs', icon: Briefcase },
    { href: '/targets', label: 'Targets', icon: Target },
    { href: '/adapters', label: 'Adapters', icon: Puzzle },
    { href: '/apps', label: 'Apps', icon: AppWindow },
    { href: '/devices', label: 'Devices', icon: Smartphone },
    { href: '/failed', label: 'Failed/DLQ', icon: AlertTriangle },
    { href: '/browser', label: 'Browser Sessions', icon: Globe },
    { href: '/webhooks', label: 'Webhooks', icon: Webhook },
    { href: '/templates', label: 'Templates', icon: FileText },
    { href: '/workflows', label: 'Workflows', icon: GitBranch },
    { href: '/cache', label: 'Cache', icon: Database },
    { href: '/audit', label: 'Audit', icon: Shield },
    { href: '/users', label: 'Users & Roles', icon: Users },
    { href: '/quotas', label: 'Quotas & Billing', icon: CreditCard },
    { href: '/settings', label: 'Settings', icon: Settings2 },
    { href: '/metrics', label: 'Metrics', icon: BarChart3 },
  ];

  // Health polling state
  let health = $state<HealthResponse | null>(null);
  let healthError = $state(false);
  let sidebarOpen = $state(false);
  let isDark = $state(true);

  async function checkHealth() {
    const res = await api.get<HealthResponse>('/v1/health');
    if (res.status === 200 && res.data) {
      health = res.data;
      healthError = false;
    } else {
      healthError = true;
    }
  }

  function toggleTheme() {
    isDark = !isDark;
    document.documentElement.classList.toggle('dark', isDark);
  }

  onMount(() => {
    // Initial theme from html class
    isDark = document.documentElement.classList.contains('dark');
    // Start health polling
    checkHealth();
    const interval = setInterval(checkHealth, 30_000);
    // Register service worker
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js').catch(() => {});
    }
    return () => clearInterval(interval);
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
      class="fixed inset-0 z-30 bg-ink/30 lg:hidden"
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
      <ul class="space-y-0.5 px-2">
        {#each navItems as item}
          {@const currentPath = $page.url.pathname}
          {@const isActive = currentPath === item.href || (item.href !== '/' && currentPath.startsWith(item.href))}
          <li>
            <a
              href={item.href}
              class="flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors duration-100"
              class:bg-accent-soft={isActive}
              class:text-accent-deep={isActive}
              class:font-medium={isActive}
              class:text-ink-soft={!isActive}
              class:hover:bg-rule-soft={!isActive}
              aria-current={isActive ? 'page' : undefined}
              onclick={() => { sidebarOpen = false; }}
            >
              <item.icon class="w-4 h-4 shrink-0" aria-hidden="true" />
              {item.label}
            </a>
          </li>
        {/each}
      </ul>
    </nav>

    <!-- Sidebar footer: gateway URL -->
    <div class="px-4 py-3 border-t border-rule text-xs font-mono text-ink-mute truncate">
      {$settings.gatewayUrl}
    </div>
  </aside>

  <!-- Main content area -->
  <div class="flex flex-col flex-1 min-w-0 overflow-hidden">
    <!-- Top bar -->
    <header class="flex items-center gap-3 px-4 py-3 border-b border-rule bg-paper-soft shrink-0">
      <!-- Mobile menu toggle -->
      <button
        class="lg:hidden p-1.5 rounded-md hover:bg-rule-soft transition-colors"
        onclick={() => (sidebarOpen = !sidebarOpen)}
        aria-label={sidebarOpen ? 'Close navigation' : 'Open navigation'}
        aria-expanded={sidebarOpen}
      >
        {#if sidebarOpen}
          <X class="w-5 h-5" aria-hidden="true" />
        {:else}
          <Menu class="w-5 h-5" aria-hidden="true" />
        {/if}
      </button>

      <!-- Health indicator -->
      <div class="flex items-center gap-2 text-xs font-mono" aria-live="polite" aria-label="Connection status">
        <span
          class="w-2 h-2 rounded-full shrink-0"
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

      <!-- Theme toggle -->
      <button
        class="p-1.5 rounded-md hover:bg-rule-soft transition-colors"
        onclick={toggleTheme}
        aria-label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
      >
        {#if isDark}
          <Sun class="w-4 h-4 text-ink-soft" aria-hidden="true" />
        {:else}
          <Moon class="w-4 h-4 text-ink-soft" aria-hidden="true" />
        {/if}
      </button>
    </header>

    <!-- Page content -->
    <main id="main-content" class="flex-1 overflow-y-auto p-6" tabindex="-1">
      {@render children()}
    </main>
  </div>
</div>
