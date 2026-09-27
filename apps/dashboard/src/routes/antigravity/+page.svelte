<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { ExternalLink, LogIn, Plus, RefreshCw, Play, Trash2, X } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import {
    addAntigravityAccount, removeAntigravityAccount, updateAntigravityAccount,
    updateAntigravityConfig, testAntigravity, getAntigravityLogin,
    startAntigravityLogin, submitAntigravityCode, stopAntigravityLogin,
    type AntigravityAccount, type AntigravityConfig, type AntigravityLoginSession,
  } from '$lib/api/antigravity';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let accounts = $state<AntigravityAccount[]>([]);
  let config = $state<AntigravityConfig | null>(null);
  let loading = $state(true);
  let denied = $state(false);
  let signInRequired = $state(false);
  let error = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let showAddForm = $state(false);
  let label = $state('');
  let tier = $state('pro');
  let adding = $state(false);
  let busyAccount = $state('');
  let model = $state('');
  let effort = $state('high');
  let saving = $state(false);
  let applied = $state(false);
  let acceptedJob = $state<{ id: string; label: string } | null>(null);
  let loginAccountID = $state('');
  let loginSession = $state<AntigravityLoginSession | null>(null);
  let authorizationCode = $state('');
  let codeSubmitted = $state(false);
  let loginBusy = $state(false);
  let loginError = $state<string | null>(null);
  let pollingLogin = false;
  let pollingVerification = false;

  function formatTime(value: string | null): string {
    if (!value || value.startsWith('0001-')) return 'Never';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? 'Unknown' : date.toLocaleString();
  }

  function coolingDown(account: AntigravityAccount): boolean {
    return !!account.cooldown_until && Date.parse(account.cooldown_until) > Date.now();
  }

  function safeAuthorizationURL(raw: string | undefined): string | null {
    if (!raw || raw.length > 4096) return null;
    try {
      const link = new URL(raw);
      const host = link.hostname;
      if (link.protocol !== 'https:' || link.username || link.password || link.hash ||
          (link.port && link.port !== '443') ||
          !(host === 'accounts.google.com' || host === 'antigravity.google' || host.endsWith('.antigravity.google'))) {
        return null;
      }
      for (const key of link.searchParams.keys()) {
        if (['code', 'access_token', 'refresh_token', 'id_token', 'client_secret', 'token'].includes(key.toLowerCase())) return null;
      }
      return raw;
    } catch {
      return null;
    }
  }

  function failure(cause: unknown): string {
    return cause instanceof Error ? cause.message : 'Gateway request failed';
  }

  async function load() {
    loading = true;
    denied = false;
    signInRequired = false;
    error = null;
    const [accountsRes, configRes] = await Promise.all([
      api.get<{ accounts: AntigravityAccount[] }>('/v1/antigravity/accounts'),
      api.get<AntigravityConfig>('/v1/antigravity/config'),
    ]);
    loading = false;
    if (accountsRes.denied || configRes.denied) { denied = true; return; }
    if (accountsRes.unauthorized || configRes.unauthorized) {
      signInRequired = true;
      return;
    }
    if (accountsRes.error || configRes.error || !accountsRes.data || !configRes.data) {
      error = accountsRes.error || configRes.error || 'The gateway did not return account settings.';
      return;
    }
    accounts = accountsRes.data.accounts ?? [];
    if (loginAccountID && !accounts.some((account) => account.account_id === loginAccountID)) {
      loginAccountID = '';
      loginSession = null;
      authorizationCode = '';
    }
    if (!config) {
      model = configRes.data.default_model;
      effort = configRes.data.default_effort;
    }
    config = configRes.data;
  }

  async function addAccount(event: SubmitEvent) {
    event.preventDefault();
    if (!label.trim() || accounts.length >= 3) return;
    adding = true;
    actionError = null;
    try {
      await addAntigravityAccount({ label: label.trim(), tier });
      label = '';
      showAddForm = false;
      await load();
    } catch (cause) {
      actionError = failure(cause);
    } finally {
      adding = false;
    }
  }

  async function toggleAccount(account: AntigravityAccount) {
    busyAccount = account.account_id;
    actionError = null;
    try {
      await updateAntigravityAccount(account.account_id, { enabled: !account.enabled });
      await load();
    } catch (cause) {
      actionError = failure(cause);
    } finally {
      busyAccount = '';
    }
  }

  let pendingRemove = $state<AntigravityAccount | null>(null);
  let pendingRemoveOpen = $state(false);

  function removeAccount(account: AntigravityAccount) {
    pendingRemove = account;
    pendingRemoveOpen = true;
  }

  async function doRemoveAccount() {
    const account = pendingRemove;
    pendingRemoveOpen = false;
    pendingRemove = null;
    if (!account) return;
    busyAccount = account.account_id;
    actionError = null;
    try {
      await removeAntigravityAccount(account.account_id);
      await load();
    } catch (cause) {
      actionError = failure(cause);
    } finally {
      busyAccount = '';
    }
  }

  async function runCanary(account: AntigravityAccount) {
    busyAccount = account.account_id;
    actionError = null;
    acceptedJob = null;
    try {
      const result = await testAntigravity('Reply with OK.', account.account_id);
      acceptedJob = { id: result.job_id, label: account.label };
      await load();
    } catch (cause) {
      actionError = failure(cause);
    } finally {
      busyAccount = '';
    }
  }

  async function startLogin(account: AntigravityAccount) {
    if (!account.worker_socket_present || loginBusy || (loginAccountID && loginAccountID !== account.account_id)) return;
    loginAccountID = account.account_id;
    loginSession = { state: 'starting' };
    loginError = null;
    codeSubmitted = false;
    authorizationCode = '';
    loginBusy = true;
    try {
      loginSession = await startAntigravityLogin(account.account_id);
      accounts = accounts.map((item) => item.account_id === account.account_id
        ? { ...item, verification_state: 'unverified', verified_at: undefined, verification_job_id: undefined }
        : item);
    } catch (cause) {
      loginError = failure(cause);
      loginSession = null;
    } finally {
      loginBusy = false;
    }
  }

  async function submitCode(event: SubmitEvent) {
    event.preventDefault();
    if (!loginAccountID || loginSession?.state !== 'awaiting_code' || !/^[A-Za-z0-9-]{1,128}$/.test(authorizationCode)) return;
    const input = authorizationCode;
    authorizationCode = '';
    loginBusy = true;
    loginError = null;
    try {
      loginSession = await submitAntigravityCode(loginAccountID, input);
      codeSubmitted = true;
    } catch (cause) {
      loginError = failure(cause);
    } finally {
      loginBusy = false;
    }
  }

  async function stopLogin() {
    if (!loginAccountID || loginBusy) return;
    loginBusy = true;
    loginError = null;
    try {
      await stopAntigravityLogin(loginAccountID);
      loginAccountID = '';
      loginSession = null;
      authorizationCode = '';
    } catch (cause) {
      loginError = failure(cause);
    } finally {
      loginBusy = false;
    }
  }

  async function finishAndTest(account: AntigravityAccount) {
    if (loginBusy || busyAccount) return;
    loginBusy = true;
    loginError = null;
    try {
      await stopAntigravityLogin(account.account_id);
      loginAccountID = '';
      loginSession = null;
      authorizationCode = '';
      await runCanary(account);
    } catch (cause) {
      loginError = failure(cause);
    } finally {
      loginBusy = false;
    }
  }

  async function pollLiveState() {
    if (loginAccountID && loginSession && !loginBusy && !loginError && !pollingLogin &&
        ['starting', 'awaiting_code', 'verifying'].includes(loginSession.state)) {
      pollingLogin = true;
      const accountID = loginAccountID;
      const observedState = loginSession.state;
      try {
        const next = await getAntigravityLogin(accountID);
        if (loginAccountID === accountID && loginSession?.state === observedState && !loginBusy) loginSession = next;
      } catch (cause) {
        if (loginAccountID === accountID) loginError = failure(cause);
      } finally {
        pollingLogin = false;
      }
    }
    if (!loading && !pollingVerification && accounts.some((account) => account.verification_state === 'pending')) {
      pollingVerification = true;
      try {
        const response = await api.get<{ accounts: AntigravityAccount[] }>('/v1/antigravity/accounts');
        if (response.data && !response.error) accounts = response.data.accounts ?? [];
        else if (response.unauthorized) signInRequired = true;
        else if (response.denied) denied = true;
        else actionError = response.error || 'Could not refresh account verification.';
      } finally {
        pollingVerification = false;
      }
    }
  }

  async function applyDefaults(event: SubmitEvent) {
    event.preventDefault();
    saving = true;
    applied = false;
    actionError = null;
    try {
      await updateAntigravityConfig({ default_model: model.trim(), default_effort: effort });
      applied = true;
    } catch (cause) {
      actionError = failure(cause);
    } finally {
      saving = false;
    }
  }

  onMount(() => {
    void load();
    const timer = window.setInterval(() => { void pollLiveState(); }, 3000);
    return () => { window.clearInterval(timer); authorizationCode = ''; };
  });
</script>

<svelte:head>
  <title>Antigravity accounts | UBAG</title>
</svelte:head>

<ConfirmDialog
  bind:open={pendingRemoveOpen}
  title="Remove account slot"
  message={pendingRemove ? 'Remove ' + pendingRemove.label + '? Its isolated worker must be deprovisioned separately.' : ''}
  confirmLabel="Remove"
  danger
  onconfirm={doRemoveAccount}
/>

<div class="mx-auto max-w-5xl space-y-7 min-w-0">
  <header class="flex flex-wrap items-center justify-between gap-3">
    <div class="min-w-0">
      <h1 class="font-display text-2xl font-bold text-ink">Antigravity</h1>
      <p class="text-sm text-ink-soft">OAuth account slots for the isolated CLI workers</p>
    </div>
    <button type="button" onclick={load} disabled={loading} title="Refresh accounts" aria-label="Refresh accounts" class="p-2 rounded-md border border-rule text-ink-soft hover:bg-rule-soft active:bg-paper-warm disabled:opacity-50">
      <RefreshCw class="w-4 h-4 {loading ? 'animate-spin' : ''}" aria-hidden="true" />
    </button>
  </header>

  {#if loading}
    <p class="text-sm text-ink-mute" role="status">Loading account settings...</p>
  {:else if denied}
    <DeniedPanel resource="Antigravity accounts" />
  {:else if signInRequired}
    <section role="alert" aria-label="Sign in required" class="border-y border-rule bg-paper-soft p-5 space-y-2 text-sm">
      <h2 class="font-medium text-ink">Sign in to manage Antigravity accounts.</h2>
      <p class="text-ink-soft">In <a href="{base}/settings" class="text-accent-deep underline underline-offset-2 hover:text-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent">Settings</a>, use this dashboard's origin as the Gateway URL and enter the gateway's admin app secret. Then return here.</p>
    </section>
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    <section aria-labelledby="runtime-heading" class="border-y border-rule bg-paper-soft py-4 px-4 space-y-2">
      <div class="flex flex-wrap items-center gap-3">
        <h2 id="runtime-heading" class="font-display font-semibold text-ink">OAuth CLI</h2>
        <span class="text-xs font-medium px-2 py-0.5 rounded-sm {config?.oauth_enabled ? 'bg-success-soft text-success' : 'bg-paper-warm text-ink-soft'}">
          {config?.oauth_enabled === true ? 'Enabled on gateway' : config?.oauth_enabled === false ? 'Disabled on gateway' : 'OAuth status unavailable'}
        </span>
      </div>
      <p class="text-sm text-ink-soft">Sign in to a provisioned worker with your Google account. A completed account-pinned test job is the verification signal.</p>
      <p class="text-xs text-ink-mute">A prior canary pass does not prove the session is still valid. Quota data is not available from the CLI.</p>
    </section>

    {#if actionError}
      <p role="alert" class="border-l-2 border-danger bg-danger-soft px-3 py-2 text-sm text-danger">{actionError}</p>
    {/if}
    {#if acceptedJob}
      <p role="status" class="border-l-2 border-success bg-success-soft px-3 py-2 text-sm text-ink">
        Canary for {acceptedJob.label} accepted as job <span class="font-mono break-all">{acceptedJob.id}</span>. Acceptance is not completion.
        <a href="{base}/jobs" class="text-accent-deep underline underline-offset-2 hover:text-accent">View jobs</a> for the outcome.
      </p>
    {/if}

    <section aria-labelledby="accounts-heading" class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="accounts-heading" class="font-display font-semibold text-lg text-ink">Account slots</h2>
          <p class="text-sm text-ink-mute">{accounts.length} / 3 configured for this tenant</p>
        </div>
        <button type="button" onclick={() => (showAddForm = !showAddForm)} disabled={accounts.length >= 3} class="inline-flex items-center gap-2 rounded-md bg-accent px-3 py-2 text-sm font-medium text-paper-soft hover:bg-accent-deep active:bg-accent-deep disabled:opacity-50 whitespace-nowrap" aria-expanded={showAddForm}>
          <Plus class="w-4 h-4" aria-hidden="true" /> Add slot
        </button>
      </div>

      {#if showAddForm}
        <form onsubmit={addAccount} class="border-y border-rule bg-paper-soft px-4 py-4 space-y-3">
          <h3 class="font-medium text-ink">New account slot</h3>
          <p class="text-xs text-ink-mute">This saves a label and tier only. It does not create a login or worker.</p>
          <div class="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,10rem)]">
            <label class="block min-w-0 text-sm text-ink-soft">Label
              <input bind:value={label} required maxlength="80" placeholder="Personal Pro" class="mt-1 block w-full rounded-sm border border-rule bg-paper px-3 py-2 text-ink focus-visible:border-accent" />
            </label>
            <label class="block text-sm text-ink-soft">Tier (unverified label)
              <select bind:value={tier} class="mt-1 block w-full rounded-sm border border-rule bg-paper px-3 py-2 text-ink">
                <option value="free">Free</option><option value="pro">Pro</option><option value="ultra">Ultra</option>
              </select>
            </label>
          </div>
          <div class="flex flex-wrap gap-2">
            <button type="submit" disabled={adding || !label.trim()} class="rounded-md bg-accent px-3 py-2 text-sm font-medium text-paper-soft hover:bg-accent-deep active:bg-accent-deep disabled:opacity-50 whitespace-nowrap">{adding ? 'Adding...' : 'Create slot'}</button>
            <button type="button" onclick={() => (showAddForm = false)} disabled={adding} class="rounded-md border border-rule px-3 py-2 text-sm text-ink hover:bg-rule-soft active:bg-paper-warm disabled:opacity-50 whitespace-nowrap">Cancel</button>
          </div>
        </form>
      {/if}

      {#if accounts.length === 0}
        <div class="border-y border-rule py-8 text-sm text-ink-mute">No OAuth account slots configured.</div>
      {:else}
        <ul class="border-y border-rule divide-y divide-rule">
          {#each accounts as account (account.account_id)}
            <li class="py-4 space-y-3 min-w-0">
              <div class="flex flex-wrap items-start justify-between gap-3">
                <div class="min-w-0">
                  <p class="font-medium text-ink break-words">{account.label}</p>
                  <p class="text-xs text-ink-mute font-mono break-all">{account.account_id} · {account.tier.toUpperCase()} (unverified)</p>
                </div>
                <div class="flex items-center gap-3">
                  <label class="inline-flex items-center gap-2 text-xs text-ink-soft cursor-pointer whitespace-nowrap">
                    <input type="checkbox" checked={account.enabled} onchange={() => toggleAccount(account)} disabled={busyAccount !== ''} class="accent-accent h-4 w-4" aria-label="Enable {account.label}" />
                    Enabled
                  </label>
                  <button type="button" onclick={() => removeAccount(account)} disabled={busyAccount !== ''} title="Remove {account.label}" aria-label="Remove {account.label}" class="rounded-sm p-2 text-danger hover:bg-danger-soft active:bg-paper-warm disabled:opacity-50">
                    <Trash2 class="w-4 h-4" aria-hidden="true" />
                  </button>
                </div>
              </div>
              <dl class="flex flex-wrap gap-x-6 gap-y-2 text-xs">
                <div><dt class="text-ink-mute">Worker socket</dt><dd class="text-ink-soft">{account.worker_socket_present ? 'Present' : 'Not found'}</dd></div>
                <div><dt class="text-ink-mute">Authentication</dt><dd class="text-ink-soft" aria-live="polite">
                  {#if !account.worker_socket_present}Worker unavailable
                  {:else if account.verification_state === 'verified' && account.verified_at}Canary passed {formatTime(account.verified_at)}
                  {:else if account.verification_state === 'pending'}Canary pending
                  {:else if account.verification_state === 'failed'}Canary failed
                  {:else}Not verified{/if}
                </dd></div>
                <div><dt class="text-ink-mute">Last attempted</dt><dd class="text-ink-soft">{formatTime(account.last_used)}</dd></div>
                <div><dt class="text-ink-mute">Quota</dt><dd class="text-ink-soft">{coolingDown(account) ? `Cooldown until ${formatTime(account.cooldown_until)}` : 'Unknown'}</dd></div>
              </dl>
              <div class="flex flex-wrap items-center gap-2">
                <button type="button" onclick={() => startLogin(account)} disabled={!account.worker_socket_present || busyAccount !== '' || loginBusy || (!!loginAccountID && loginAccountID !== account.account_id)} aria-expanded={loginAccountID === account.account_id} title="Sign in to {account.label}" class="inline-flex items-center gap-2 rounded-md bg-accent px-3 py-2 text-sm font-medium text-paper-soft hover:bg-accent-deep active:bg-accent-deep disabled:opacity-50 whitespace-nowrap">
                  <LogIn class="w-4 h-4" aria-hidden="true" /> {loginBusy && loginAccountID === account.account_id ? 'Starting...' : 'Sign in'}
                </button>
                <button type="button" onclick={() => runCanary(account)} disabled={busyAccount !== '' || !config?.oauth_enabled || !account.enabled || !account.worker_socket_present || coolingDown(account) || (loginAccountID === account.account_id && loginSession?.state !== 'closed')} title="Send a real test prompt with {account.label}" class="inline-flex items-center gap-2 rounded-md border border-rule px-3 py-2 text-sm text-ink hover:bg-rule-soft active:bg-paper-warm disabled:opacity-50 whitespace-nowrap">
                  <Play class="w-3.5 h-3.5" aria-hidden="true" /> {busyAccount === account.account_id ? 'Submitting...' : 'Test account'}
                </button>
              </div>
              {#if loginAccountID === account.account_id}
                <div role="region" aria-label="Sign in to {account.label}" class="border-l-2 border-accent bg-paper-soft px-4 py-3 space-y-3 min-w-0">
                  <div class="flex flex-wrap items-center justify-between gap-2">
                    <h3 class="font-display text-sm font-semibold text-ink">Google authorization for {account.label}</h3>
                    <button type="button" onclick={stopLogin} disabled={loginBusy} title="Stop sign-in" class="inline-flex items-center gap-1 rounded-sm px-2 py-1 text-xs text-ink-soft hover:bg-rule-soft active:bg-paper-warm disabled:opacity-50">
                      <X class="w-4 h-4" aria-hidden="true" /> Stop sign-in
                    </button>
                  </div>
                  {#if loginError}<p role="alert" class="text-sm text-danger">{loginError}</p>{/if}
                  {#if loginSession?.state === 'starting'}
                    <p role="status" class="text-sm text-ink-soft">Starting Google authorization...</p>
                  {:else if loginSession?.state === 'awaiting_code'}
                    {@const authorizationURL = safeAuthorizationURL(loginSession.authorization_url)}
                    {#if authorizationURL}
                      <a href={authorizationURL} target="_blank" rel="noopener noreferrer" title="Open Google authorization in a new tab" class="inline-flex items-center gap-2 text-sm font-medium text-accent-deep underline underline-offset-2 hover:text-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent">
                        <ExternalLink class="w-4 h-4" aria-hidden="true" /> Open Google sign-in
                      </a>
                      <form onsubmit={submitCode} class="flex flex-wrap items-end gap-2">
                        <label class="min-w-0 flex-1 basis-48 text-sm text-ink-soft">Authorization code
                          <input type="password" bind:value={authorizationCode} required maxlength="128" pattern="[A-Za-z0-9-]+" autocomplete="off" spellcheck="false" class="mt-1 block w-full rounded-sm border border-rule bg-paper px-3 py-2 text-ink focus-visible:border-accent" />
                        </label>
                        <button type="submit" disabled={loginBusy || !authorizationCode} class="inline-flex items-center gap-2 rounded-md bg-accent px-3 py-2 text-sm font-medium text-paper-soft hover:bg-accent-deep active:bg-accent-deep disabled:opacity-50 whitespace-nowrap">Submit code</button>
                      </form>
                    {:else}
                      <p role="alert" class="text-sm text-danger">No safe Google authorization link is available. Stop sign-in and try again.</p>
                    {/if}
                  {:else if loginSession?.state === 'verifying' || loginSession?.state === 'closed'}
                    <p role="status" class="text-sm text-ink-soft">{codeSubmitted ? 'Code submitted' : 'CLI session closed or awaiting verification'}</p>
                    <button type="button" onclick={() => finishAndTest(account)} disabled={loginBusy || !config?.oauth_enabled || !account.enabled || coolingDown(account)} title="Stop the login CLI and send an account-pinned test prompt" class="inline-flex items-center gap-2 rounded-md border border-rule px-3 py-2 text-sm text-ink hover:bg-rule-soft active:bg-paper-warm disabled:opacity-50 whitespace-nowrap">
                      <Play class="w-3.5 h-3.5" aria-hidden="true" /> Verify with test job
                    </button>
                    {#if !config?.oauth_enabled}<p class="text-xs text-ink-mute">OAuth jobs must be enabled on the gateway before verification.</p>{/if}
                  {/if}
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
      <p class="text-xs text-ink-mute">Eligible accounts rotate automatically. A different slot is tried only when the CLI reports a structured quota rejection before any output; an account-specific test never switches slots.</p>
    </section>

    {#if config}
      <section aria-labelledby="defaults-heading" class="border-t border-rule pt-5 space-y-3">
        <div>
          <h2 id="defaults-heading" class="font-display font-semibold text-lg text-ink">Canary defaults</h2>
          <p class="text-sm text-ink-mute">Used for dashboard test jobs. Changes apply to this gateway process only and reset on restart.</p>
        </div>
        <form onsubmit={applyDefaults} class="flex flex-wrap items-end gap-3">
          <label class="min-w-0 flex-1 basis-48 text-sm text-ink-soft">CLI model ID
            <input bind:value={model} required maxlength="80" pattern="[A-Za-z0-9][A-Za-z0-9_.-]*" class="mt-1 block w-full rounded-sm border border-rule bg-paper-soft px-3 py-2 text-ink" />
          </label>
          <label class="min-w-0 flex-1 basis-36 text-sm text-ink-soft">Thinking effort
            <select bind:value={effort} class="mt-1 block w-full rounded-sm border border-rule bg-paper-soft px-3 py-2 text-ink">
              <option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option><option value="max">Max</option>
            </select>
          </label>
          <button type="submit" disabled={saving} class="rounded-md bg-accent px-3 py-2 text-sm font-medium text-paper-soft hover:bg-accent-deep active:bg-accent-deep disabled:opacity-50 whitespace-nowrap">{saving ? 'Applying...' : 'Apply defaults'}</button>
        </form>
        {#if applied}<p role="status" class="text-sm text-success">Applied for this gateway process.</p>{/if}
      </section>
    {/if}

    <section aria-labelledby="quota-heading" class="border-t border-rule pt-5">
      <h2 id="quota-heading" class="font-display font-semibold text-lg text-ink">Subscription quota</h2>
      <p class="text-sm text-ink-mute mt-1">Remaining usage and reset times are unavailable. The gateway does not collect verified OAuth quota data; the cooldown above only records an observed upfront rejection.</p>
    </section>
  {/if}
</div>