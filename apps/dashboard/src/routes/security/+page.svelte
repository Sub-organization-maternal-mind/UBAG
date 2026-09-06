<script lang="ts">
  import { api } from '$lib/api/client';
  import EmptyState from '$lib/components/EmptyState.svelte';

  // --- PAT issuance (POST /v1/auth/pat, auth:pat:issue = superadmin only) ---
  interface PatIssued {
    token: string;
    tenant_id: string;
    app_id: string;
    role: string;
    expires_at?: string | null;
  }
  let patTenant = $state('');
  let patApp = $state('');
  let patRole = $state('service');
  let patTtl = $state('');
  let patLoading = $state(false);
  let patError = $state<string | null>(null);
  let patIssued = $state<PatIssued | null>(null);
  let patCopied = $state(false);

  async function doIssuePat() {
    const ttl = Number.parseInt(patTtl, 10);
    patLoading = true;
    patError = null;
    patIssued = null;
    patCopied = false;
    const body: Record<string, unknown> = {
      role: patRole,
      ...(patTenant.trim() ? { tenant_id: patTenant.trim() } : {}),
      ...(patApp.trim() ? { app_id: patApp.trim() } : {}),
      ...(Number.isFinite(ttl) && ttl > 0 ? { ttl_seconds: ttl } : {}),
    };
    const res = await api.post<PatIssued>('/v1/auth/pat', body);
    patLoading = false;
    if (res.error) {
      patError = res.status === 501
        ? 'PAT issuance is not enabled on this gateway (UBAG_PAT_ENABLED).'
        : res.status === 403
          ? 'Issuing PATs requires the superadmin role.'
          : `Issuance failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    patIssued = (res.data as PatIssued | null) ?? null;
  }

  async function copyToken() {
    if (!patIssued?.token) return;
    try {
      await navigator.clipboard.writeText(patIssued.token);
      patCopied = true;
      setTimeout(() => (patCopied = false), 2500);
    } catch {
      patCopied = false;
    }
  }

  // --- MFA verify (POST /v1/mfa/verify, marks the current session) ---
  let mfaCode = $state('');
  let mfaLoading = $state(false);
  let mfaError = $state<string | null>(null);
  let mfaVerified = $state(false);

  async function doVerifyMfa() {
    const code = mfaCode.trim();
    if (!code) {
      mfaError = 'Enter the 6-digit code from your authenticator.';
      return;
    }
    mfaLoading = true;
    mfaError = null;
    const res = await api.post<{ verified?: boolean }>('/v1/mfa/verify', { code });
    mfaLoading = false;
    if (res.error) {
      mfaError = res.status === 501
        ? 'MFA is not enabled on this gateway.'
        : res.status === 401
          ? 'That code is invalid or expired. Try the next one.'
          : `Verification failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    mfaVerified = true;
    mfaCode = '';
  }

  // --- SSO logout (POST /v1/sso/logout, revokes the current gateway session) ---
  let logoutLoading = $state(false);
  let logoutError = $state<string | null>(null);
  let logoutDone = $state(false);

  async function doSsoLogout() {
    logoutLoading = true;
    logoutError = null;
    const res = await api.post<{ revoked?: boolean }>('/v1/sso/logout', {});
    logoutLoading = false;
    if (res.error) {
      logoutError = res.status === 501
        ? 'SSO sessions are not enabled on this gateway.'
        : `Logout failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    logoutDone = true;
  }
</script>

<div class="space-y-8">
  <h1 class="text-2xl font-display font-bold text-ink">Security</h1>
  <p class="text-sm text-ink-soft max-w-2xl">
    Credentials and session controls: personal access tokens, TOTP verification
    for the current session, and SSO logout. Token material is shown once —
    store it before leaving this page.
  </p>

  <!-- PAT issuance -->
  <section aria-labelledby="pat-heading">
    <h2 id="pat-heading" class="text-lg font-display font-semibold text-ink mb-3">Personal access tokens</h2>
    <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-3 max-w-xl">
      <p class="text-xs text-ink-mute">
        Tenant/app overrides are honored for privileged callers; otherwise the token
        inherits your scope. TTL defaults to the gateway's configured default when empty.
      </p>
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Tenant override</span>
          <input type="text" bind:value={patTenant} placeholder="e.g. tenant_radiology" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">App override</span>
          <input type="text" bind:value={patApp} placeholder="e.g. radiology-assist" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Role</span>
          <select bind:value={patRole} class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40">
            <option value="service">service</option>
            <option value="developer">developer</option>
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </select>
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">TTL (seconds, empty = default)</span>
          <input type="number" min="60" step="60" bind:value={patTtl} placeholder="3600" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
      </div>
      <button
        onclick={() => doIssuePat()}
        disabled={patLoading}
        class="px-4 py-2 rounded-md bg-accent text-paper-soft text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {patLoading ? 'Issuing…' : 'Issue token'}
      </button>
      {#if patError}
        <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{patError}</div>
      {/if}
      {#if patIssued}
        <div class="rounded-md border border-success/30 bg-success-soft p-4 space-y-2" role="status">
          <p class="text-sm font-medium text-success">Token issued — copy it now, it is not retrievable later.</p>
          <div class="flex items-center gap-2">
            <code class="flex-1 block text-xs font-mono text-ink bg-paper border border-rule rounded-md px-3 py-2 break-all select-all">{patIssued.token}</code>
            <button
              onclick={() => copyToken()}
              class="px-3 py-2 rounded-md border border-rule bg-paper-soft text-ink text-xs font-medium hover:bg-paper-warm transition-colors shrink-0"
            >{patCopied ? 'Copied ✓' : 'Copy'}</button>
          </div>
          <p class="text-xs text-ink-soft font-mono">
            scope: {patIssued.tenant_id}/{patIssued.app_id} · role: {patIssued.role}
            {#if patIssued.expires_at} · expires {patIssued.expires_at}{/if}
          </p>
        </div>
      {/if}
    </div>
  </section>

  <!-- MFA verify -->
  <section aria-labelledby="mfa-verify-heading">
    <h2 id="mfa-verify-heading" class="text-lg font-display font-semibold text-ink mb-3">Verify this session (TOTP)</h2>
    <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-3 max-w-xl">
      {#if mfaVerified}
        <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">
          Session verified ✓ — subsequent privileged requests carry the MFA marker.
        </div>
      {:else}
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Authenticator code</span>
          <input
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            bind:value={mfaCode}
            placeholder="123456"
            maxlength="8"
            class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink font-mono placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
          />
        </label>
        <button
          onclick={() => doVerifyMfa()}
          disabled={mfaLoading}
          class="px-4 py-2 rounded-md bg-accent text-paper-soft text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          {mfaLoading ? 'Verifying…' : 'Verify'}
        </button>
        {#if mfaError}
          <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{mfaError}</div>
        {/if}
        <p class="text-xs text-ink-mute">
          Enroll from <a href="/users" class="text-accent-deep hover:underline">Users &amp; Roles</a> if this identity has no TOTP secret yet.
        </p>
      {/if}
    </div>
  </section>

  <!-- SSO logout -->
  <section aria-labelledby="sso-logout-heading">
    <h2 id="sso-logout-heading" class="text-lg font-display font-semibold text-ink mb-3">SSO session</h2>
    {#if logoutDone}
      <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success max-w-xl" role="status">
        Session revoked ✓ — the gateway session token no longer authenticates.
      </div>
    {:else}
      <div class="rounded-md border border-rule bg-paper-soft p-4 max-w-xl space-y-3">
        <p class="text-sm text-ink-soft">
          Revokes the gateway session minted at SSO callback (OIDC or SAML). App-secret and PAT
          credentials are unaffected.
        </p>
        <button
          onclick={() => doSsoLogout()}
          disabled={logoutLoading}
          class="px-4 py-2 rounded-md border border-rule bg-paper text-ink text-sm font-medium hover:bg-paper-warm disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          {logoutLoading ? 'Revoking…' : 'Revoke current session'}
        </button>
        {#if logoutError}
          <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{logoutError}</div>
        {/if}
      </div>
    {/if}
  </section>
</div>
