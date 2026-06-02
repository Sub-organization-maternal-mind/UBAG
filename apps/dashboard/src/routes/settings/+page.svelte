<script lang="ts">
  import { settings } from '$lib/stores/settings';
  import { api } from '$lib/api/client';
  import type { HealthResponse } from '$lib/api/types';

  let gatewayUrl = $state($settings.gatewayUrl);
  let appSecret = $state($settings.appSecret);
  let saving = $state(false);
  let saved = $state(false);
  let health = $state<HealthResponse | null>(null);
  let healthError = $state<string | null>(null);
  let testing = $state(false);

  async function save() {
    saving = true;
    settings.save(gatewayUrl, appSecret);
    saving = false;
    saved = true;
    setTimeout(() => (saved = false), 3000);
  }

  async function testConnection() {
    testing = true;
    health = null;
    healthError = null;
    const res = await api.get<HealthResponse>('/v1/health');
    if (res.status === 200 && res.data) {
      health = res.data;
    } else {
      healthError = res.error ?? `HTTP ${res.status}`;
    }
    testing = false;
  }
</script>

<div class="space-y-6 max-w-xl">
  <h1 class="text-2xl font-display font-bold text-ink">Settings</h1>

  <form onsubmit={(e) => { e.preventDefault(); save(); }} class="space-y-4">
    <div>
      <label class="block text-sm font-medium text-ink mb-1" for="gateway-url">
        Gateway URL
      </label>
      <input
        id="gateway-url"
        type="url"
        bind:value={gatewayUrl}
        placeholder="http://127.0.0.1:8081"
        class="w-full px-3 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-mono focus:outline-none focus:border-accent"
        autocomplete="off"
      />
      <p class="text-xs text-ink-mute mt-1">The UBAG gateway API base URL</p>
    </div>

    <div>
      <label class="block text-sm font-medium text-ink mb-1" for="app-secret">
        App Secret (Bearer token)
      </label>
      <input
        id="app-secret"
        type="password"
        bind:value={appSecret}
        placeholder="paste your UBAG_APP_SECRET"
        class="w-full px-3 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-mono focus:outline-none focus:border-accent"
        autocomplete="off"
      />
      <p class="text-xs text-ink-mute mt-1">
        Stored in localStorage only. Roles: viewer | service | developer | operator | admin.
        Set role with <code class="font-mono">UBAG_ACTOR_ROLE</code> env var on the gateway.
      </p>
    </div>

    <div class="flex items-center gap-3 flex-wrap">
      <button
        type="submit"
        disabled={saving}
        class="px-4 py-2 rounded-md bg-accent text-paper-soft text-sm font-medium hover:bg-accent-deep transition-colors disabled:opacity-50"
      >
        {saving ? 'Saving…' : 'Save'}
      </button>

      <button
        type="button"
        onclick={testConnection}
        disabled={testing}
        class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-rule-soft transition-colors disabled:opacity-50"
      >
        {testing ? 'Testing…' : 'Test Connection'}
      </button>

      {#if saved}
        <span class="text-success text-sm font-medium" role="status">Saved ✓</span>
      {/if}
    </div>
  </form>

  {#if health}
    <div class="rounded-md border border-success/30 bg-success-soft p-4 text-sm" role="status">
      <p class="font-medium text-success">Connected ✓</p>
      <pre class="text-ink-soft mt-1 font-mono text-xs whitespace-pre-wrap">{JSON.stringify(health, null, 2)}</pre>
    </div>
  {:else if healthError}
    <div class="rounded-md border border-danger/30 bg-danger-soft p-4 text-sm" role="alert">
      <p class="font-medium text-danger">Connection failed</p>
      <p class="text-ink-soft mt-1 font-mono text-xs">{healthError}</p>
    </div>
  {/if}

  <!-- API Version section -->
  <div class="border-t border-rule pt-6">
    <h2 class="text-lg font-display font-semibold text-ink mb-3">API Version</h2>
    <p class="text-sm text-ink-soft">
      Current: <code class="font-mono text-accent-deep">{$settings.apiVersion}</code>
    </p>
    <p class="text-xs text-ink-mute mt-1">
      The Ubag-Api-Version header sent with every request. Changing this requires a page reload.
    </p>
  </div>

  <!-- localStorage info -->
  <div class="border-t border-rule pt-6">
    <h2 class="text-lg font-display font-semibold text-ink mb-3">Storage</h2>
    <p class="text-xs text-ink-mute">
      Settings are persisted to <code class="font-mono">localStorage</code> under keys
      <code class="font-mono">ubag_gateway_url</code> and <code class="font-mono">ubag_app_secret</code>.
      They are loaded automatically on every page visit.
    </p>
  </div>
</div>
