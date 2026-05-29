<script lang="ts">
  import { settings } from "./lib/settings";
  import { hasAppSecret } from "./lib/secureStore";
  import { onMount } from "svelte";
  import OverviewView from "./components/OverviewView.svelte";
  import JobsView from "./components/JobsView.svelte";
  import JobDetailView from "./components/JobDetailView.svelte";
  import AlertsView from "./components/AlertsView.svelte";
  import SettingsView from "./components/SettingsView.svelte";

  type Tab = "overview" | "jobs" | "alerts" | "settings";

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: "overview", label: "Overview", icon: "◎" },
    { id: "jobs", label: "Jobs", icon: "▤" },
    { id: "alerts", label: "Alerts", icon: "◈" },
    { id: "settings", label: "Settings", icon: "⚙" },
  ];

  let active: Tab = "overview";
  let openJobId: string | null = null;
  let configured = false;

  onMount(async () => {
    configured = await hasAppSecret();
    // First-run: route the operator to Settings until a secret is stored.
    if (!configured) {
      active = "settings";
    }
  });

  function selectTab(tab: Tab) {
    openJobId = null;
    active = tab;
  }

  function openJob(jobId: string) {
    openJobId = jobId;
  }

  function closeJob() {
    openJobId = null;
  }

  // Re-check configuration after the user visits settings.
  $: if (active === "settings") {
    void hasAppSecret().then((v) => (configured = v));
  }
</script>

<div class="shell">
  <header class="app-header">
    <div class="brand">
      <h1>UBAG</h1>
      <span class="kicker">Monitor</span>
    </div>
    <div class="conn">
      <span class="dot" data-tone={configured ? "ready" : "warn"} aria-hidden="true"></span>
      <span class="mono" style="font-size:var(--text-xs)">{$settings.gatewayUrl}</span>
    </div>
  </header>

  <main class="main">
    {#if active === "overview"}
      <OverviewView />
    {:else if active === "jobs"}
      {#if openJobId}
        <JobDetailView jobId={openJobId} onBack={closeJob} />
      {:else}
        <JobsView onOpen={openJob} />
      {/if}
    {:else if active === "alerts"}
      <AlertsView />
    {:else if active === "settings"}
      <SettingsView />
    {/if}
  </main>

  <nav class="tabbar" aria-label="Primary">
    {#each TABS as tab}
      <button
        aria-current={active === tab.id ? "page" : undefined}
        on:click={() => selectTab(tab.id)}
      >
        <span class="icon" aria-hidden="true">{tab.icon}</span>
        {tab.label}
      </button>
    {/each}
  </nav>
</div>
