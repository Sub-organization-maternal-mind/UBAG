<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { Plus, RefreshCw, Play, Trash2 } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import {
    addAntigravityAccount, removeAntigravityAccount, updateAntigravityAccount,
    updateAntigravityConfig, testAntigravity,
    type AntigravityAccount, type AntigravityConfig,
  } from '$lib/api/antigravity';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';

  let accounts = $state<AntigravityAccount[]>([]);
  let config = $state<AntigravityConfig | null>(null);
  let loading = $state(true);
  let denied = $state(false);
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

  function formatTime(value: string | null): string {
    if (!value || value.startsWith('0001-')) return 'Never';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? 'Unknown' : date.toLocaleString();
  }

  function coolingDown(account: AntigravityAccount): boolean {
    return !!account.cooldown_until && Date.parse(account.cooldown_until) > Date.now();
  }

  function failure(cause: unknown): string {
    return cause instanceof Error ? cause.message : 'Gateway request failed';
  }

  async function load() {
    loading = true;
    denied = false;
    error = null;
    const [accountsRes, configRes] = await Promise.all([
      api.get<{ accounts: AntigravityAccount[] }>('/v1/antigravity/accounts'),
      api.get<AntigravityConfig>('/v1/antigravity/config'),
    ]);
    loading = false;
    if (accountsRes.denied || configRes.denied) { denied = true; return; }
    if (accountsRes.unauthorized || configRes.unauthorized) {
      error = 'Sign in to manage Antigravity accounts.';
      return;
    }
    if (accountsRes.error || configRes.error || !accountsRes.data || !configRes.data) {
      error = accountsRes.error || configRes.error || 'The gateway did not return account settings.';
      return;
    }
    accounts = accountsRes.data.accounts ?? [];
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

  async function removeAccount(account: AntigravityAccount) {
    if (!confirm(`Remove ${account.label}? Its isolated worker must be deprovisioned separately.`)) return;
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

  onMount(load);
</script>

<svelte:head>
  <title>Antigravity accounts | UBAG</title>
</svelte:head>

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
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    <section aria-labelledby="runtime-heading" class="border-y border-rule bg-paper-soft py-4 px-4 space-y-2">
      <div class="flex flex-wrap items-center gap-3">
        <h2 id="runtime-heading" class="font-display font-semibold text-ink">OAuth CLI</h2>
        <span class="text-xs font-medium px-2 py-0.5 rounded-sm {config?.oauth_enabled ? 'bg-success-soft text-success' : 'bg-paper-warm text-ink-soft'}">
          {config?.oauth_enabled ? 'Enabled on gateway' : 'Disabled on gateway'}
        </span>
      </div>
      <p class="text-sm text-ink-soft">Adding a slot does not sign in. Open each account worker on the server and complete the official CLI OAuth sign-in yourself. Enter any authorization code only in your terminal, never in this dashboard.</p>
      <p class="text-xs text-ink-mute">The CLI is separate from the Gemini API-key SDK. A socket being present does not confirm an authenticated account.</p>
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
                <div><dt class="text-ink-mute">Worker socket</dt><dd class="text-ink-soft">{account.worker_socket_present ? 'Present (auth unverified)' : 'Not found'}</dd></div>
                <div><dt class="text-ink-mute">Last attempted</dt><dd class="text-ink-soft">{formatTime(account.last_used)}</dd></div>
                <div><dt class="text-ink-mute">Quota</dt><dd class="text-ink-soft">{coolingDown(account) ? `Cooldown until ${formatTime(account.cooldown_until)}` : 'Unknown'}</dd></div>
              </dl>
              <button type="button" onclick={() => runCanary(account)} disabled={busyAccount !== '' || !config?.oauth_enabled || !account.enabled || !account.worker_socket_present || coolingDown(account)} title="Send a real test prompt with {account.label}" class="inline-flex items-center gap-2 rounded-md border border-rule px-3 py-2 text-sm text-ink hover:bg-rule-soft active:bg-paper-warm disabled:opacity-50 whitespace-nowrap">
                <Play class="w-3.5 h-3.5" aria-hidden="true" /> {busyAccount === account.account_id ? 'Submitting...' : 'Test account'}
              </button>
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