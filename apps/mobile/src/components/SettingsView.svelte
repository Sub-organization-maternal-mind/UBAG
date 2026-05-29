<script lang="ts">
  import { settings, normalizeGatewayUrl, DEFAULT_SETTINGS } from "../lib/settings";
  import { setAppSecret, getAppSecret, clearAppSecret } from "../lib/secureStore";
  import { getVersion } from "../lib/api";
  import { onMount } from "svelte";

  let gatewayUrl = $settings.gatewayUrl;
  let refreshSeconds = $settings.refreshSeconds;
  let liveTimeline = $settings.liveTimeline;
  let secret = "";
  let hasStoredSecret = false;
  let showSecret = false;

  let testState: "idle" | "testing" | "ok" | "fail" = "idle";
  let testMessage = "";
  let saved = false;

  onMount(async () => {
    hasStoredSecret = !!(await getAppSecret());
  });

  async function save() {
    settings.set({
      gatewayUrl: normalizeGatewayUrl(gatewayUrl),
      refreshSeconds: Math.max(5, Number(refreshSeconds) || DEFAULT_SETTINGS.refreshSeconds),
      liveTimeline,
    });
    if (secret.trim()) {
      await setAppSecret(secret.trim());
      hasStoredSecret = true;
      secret = "";
    }
    saved = true;
    setTimeout(() => (saved = false), 2000);
  }

  async function clearSecret() {
    await clearAppSecret();
    hasStoredSecret = false;
    secret = "";
  }

  async function testConnection() {
    testState = "testing";
    testMessage = "";
    // Persist current values first so the test uses what the user typed.
    await save();
    try {
      const version = await getVersion(normalizeGatewayUrl(gatewayUrl));
      testState = "ok";
      testMessage = `Reachable — ${version.service} ${version.version}`;
    } catch (err) {
      testState = "fail";
      testMessage = err instanceof Error ? err.message : "Connection failed";
    }
  }
</script>

<section aria-labelledby="settings-title">
  <p class="section-kicker">Connection</p>
  <h2 id="settings-title" class="view-title">Settings</h2>

  <div class="card">
    <div class="field">
      <label for="gateway-url">Gateway URL</label>
      <input
        id="gateway-url"
        type="url"
        inputmode="url"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        bind:value={gatewayUrl}
        placeholder={DEFAULT_SETTINGS.gatewayUrl}
      />
      <p class="hint">Base URL of the UBAG gateway REST API.</p>
    </div>

    <div class="field">
      <label for="app-secret">App secret</label>
      <div class="btn-row">
        <input
          id="app-secret"
          type={showSecret ? "text" : "password"}
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          bind:value={secret}
          placeholder={hasStoredSecret ? "•••••••• (stored securely)" : "Bearer app-secret"}
          aria-describedby="secret-hint"
        />
        <button
          type="button"
          class="icon-btn"
          aria-label={showSecret ? "Hide secret" : "Show secret"}
          on:click={() => (showSecret = !showSecret)}
        >
          {showSecret ? "🙈" : "👁"}
        </button>
      </div>
      <p class="hint" id="secret-hint">
        Sent only as <span class="mono">Authorization: Bearer …</span>. Stored in the device's
        secure store, never logged. Leave blank to keep the existing secret.
      </p>
      {#if hasStoredSecret}
        <button type="button" class="btn danger" style="margin-top: var(--space-2)" on:click={clearSecret}>
          Clear stored secret
        </button>
      {/if}
    </div>
  </div>

  <div class="card">
    <div class="field">
      <label for="refresh">Auto-refresh (seconds)</label>
      <input id="refresh" type="number" min="5" max="300" bind:value={refreshSeconds} />
    </div>
    <div class="field">
      <label for="live">
        <input id="live" type="checkbox" bind:checked={liveTimeline} />
        Live job timeline (poll while a job is open)
      </label>
    </div>
  </div>

  <div class="btn-row">
    <button class="btn" on:click={save}>{saved ? "Saved ✓" : "Save"}</button>
    <button class="btn secondary" on:click={testConnection} disabled={testState === "testing"}>
      {testState === "testing" ? "Testing…" : "Test connection"}
    </button>
  </div>

  {#if testState === "ok"}
    <div class="notice" data-tone="warn" role="status" style="margin-top: var(--space-3); border-color: var(--color-success); background: var(--color-success-soft)">
      <span class="dot" data-tone="ready" aria-hidden="true"></span>
      <div><h3>Connected</h3><p>{testMessage}</p></div>
    </div>
  {:else if testState === "fail"}
    <div class="notice" data-tone="danger" role="alert" style="margin-top: var(--space-3)">
      <span class="dot" data-tone="danger" aria-hidden="true"></span>
      <div><h3>Connection failed</h3><p>{testMessage}</p></div>
    </div>
  {/if}
</section>
