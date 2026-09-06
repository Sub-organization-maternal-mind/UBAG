<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  // --- Privacy (GDPR export / erase receipts) ---
  interface PrivacyReceipt {
    api_version?: string;
    request_id: string;
    kind: string;
    status: string;
    receipt: string;
    created_at: string;
    trace_id?: string;
  }
  let privacyRequests = $state<PrivacyReceipt[]>([]);
  let privacySubject = $state('');
  let privacyLoading = $state(false);
  let privacyError = $state<string | null>(null);
  let privacyNotice = $state<string | null>(null);

  async function doPrivacyRequest(kind: 'export' | 'erase') {
    const subjectRef = privacySubject.trim();
    if (!subjectRef) {
      privacyError = 'A subject reference is required (the data subject the request concerns).';
      return;
    }
    privacyLoading = true;
    privacyError = null;
    privacyNotice = null;
    const res = kind === 'export'
      ? await api.post<PrivacyReceipt>('/v1/privacy/export', { subject_ref: subjectRef })
      : await api.post<PrivacyReceipt>('/v1/privacy/erase', { subject_ref: subjectRef });
    privacyLoading = false;
    if (res.error) {
      privacyError = res.status === 501
        ? 'Privacy request handling is not enabled on this gateway.'
        : `Request failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    if (res.data) {
      privacyRequests = [res.data, ...privacyRequests];
      privacyNotice = `${kind === 'export' ? 'Export' : 'Erase'} request ${res.data.request_id} recorded — receipt ${res.data.receipt}.`;
    }
  }

  // --- JIT elevation: request (job:create) + approve (role:manage) ---
  interface ElevationGrant {
    id: string;
    actor?: string;
    role: string;
    reason?: string;
    ttl_seconds?: number;
    expires_at?: string;
    approved?: boolean;
    approved_by?: string;
  }
  let elevations = $state<ElevationGrant[]>([]);
  let elevRole = $state('operator');
  let elevTtl = $state('3600');
  let elevReason = $state('');
  let elevLoading = $state(false);
  let elevError = $state<string | null>(null);
  let elevNotice = $state<string | null>(null);

  async function doRequestElevation() {
    const reason = elevReason.trim();
    if (!reason) {
      elevError = 'A reason is required for the audit record.';
      return;
    }
    const ttl = Number.parseInt(elevTtl, 10);
    elevLoading = true;
    elevError = null;
    elevNotice = null;
    const res = await api.post<ElevationGrant>('/v1/admin/elevation', {
      role: elevRole,
      ttl_seconds: Number.isFinite(ttl) && ttl > 0 ? ttl : 3600,
      reason,
    });
    elevLoading = false;
    if (res.error) {
      elevError = res.status === 501
        ? 'JIT admin elevation is not enabled on this gateway.'
        : `Request failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    if (res.data) {
      elevations = [res.data, ...elevations];
      elevNotice = `Elevation grant ${res.data.id} requested (${res.data.role}). An admin must approve it.`;
    }
  }

  async function doApproveElevation(grant: ElevationGrant) {
    elevError = null;
    elevNotice = null;
    const res = await api.post<ElevationGrant>(`/v1/admin/elevation/${grant.id}/approve`, {});
    if (res.error) {
      elevError = `Approve failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    elevNotice = `Grant ${grant.id} approved.`;
  }

  // --- Region kill switch state (region:manage) ---
  type RegionStateName = 'active' | 'draining' | 'disabled';
  let regionName = $state('');
  let regionState = $state<RegionStateName>('active');
  let regionLoading = $state(false);
  let regionError = $state<string | null>(null);
  let regionNotice = $state<string | null>(null);

  async function doSetRegionState() {
    const name = regionName.trim();
    if (!name) {
      regionError = 'A region name is required (e.g. eu-central).';
      return;
    }
    regionLoading = true;
    regionError = null;
    regionNotice = null;
    const res = await api.post<{ region?: string; state?: string }>(
      `/v1/admin/regions/${encodeURIComponent(name)}/state`,
      { state: regionState }
    );
    regionLoading = false;
    if (res.error) {
      regionError = res.status === 501
        ? 'The region kill switch is not enabled on this gateway.'
        : `Failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    regionNotice = `Region ${name} set to ${regionState}.`;
  }

  onMount(() => {});
</script>

<div class="space-y-8">
  <h1 class="text-2xl font-display font-bold text-ink">Administration</h1>
  <p class="text-sm text-ink-soft max-w-2xl">
    Privileged operations: GDPR subject requests, just-in-time admin elevation,
    and the region kill switch. Every action here is audit-logged by the gateway.
  </p>

  <!-- Privacy requests -->
  <section aria-labelledby="privacy-heading">
    <h2 id="privacy-heading" class="text-lg font-display font-semibold text-ink mb-3">Privacy requests (GDPR)</h2>
    <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-3 max-w-xl">
      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Subject reference *</span>
        <input
          type="text"
          bind:value={privacySubject}
          placeholder="e.g. user_42 or device_id:abc123"
          class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
        />
      </label>
      <div class="flex items-center gap-3 flex-wrap">
        <button
          onclick={() => doPrivacyRequest('export')}
          disabled={privacyLoading}
          class="px-4 py-2 rounded-md border border-rule bg-paper text-ink text-sm font-medium hover:bg-paper-warm disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          {privacyLoading ? 'Working…' : 'Request export'}
        </button>
        <button
          onclick={() => doPrivacyRequest('erase')}
          disabled={privacyLoading}
          class="px-4 py-2 rounded-md bg-danger text-paper-soft text-sm font-medium hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          Request erase
        </button>
      </div>
      {#if privacyError}
        <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{privacyError}</div>
      {/if}
      {#if privacyNotice}
        <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">{privacyNotice}</div>
      {/if}
    </div>

    {#if privacyRequests.length > 0}
      <div class="mt-4 rounded-md border border-rule overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Request</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Kind</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Receipt</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each privacyRequests as req (req.request_id)}
              <tr class="hover:bg-paper-soft transition-colors">
                <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{req.request_id}</td>
                <td class="px-4 py-2.5 text-xs text-ink font-medium">{req.kind}</td>
                <td class="px-4 py-2.5 text-xs text-ink-soft">{req.status}</td>
                <td class="px-4 py-2.5 font-mono text-xs text-ink-soft break-all">{req.receipt}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <!-- JIT elevation -->
  <section aria-labelledby="elevation-heading">
    <h2 id="elevation-heading" class="text-lg font-display font-semibold text-ink mb-3">Just-in-time elevation</h2>
    <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-3 max-w-xl">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Role</span>
          <select bind:value={elevRole} class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40">
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </select>
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">TTL (seconds)</span>
          <input type="number" min="60" step="60" bind:value={elevTtl} class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
      </div>
      <label class="block">
        <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Reason *</span>
        <input
          type="text"
          bind:value={elevReason}
          placeholder="e.g. incident response — disk pressure on gateway host"
          class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
        />
      </label>
      <button
        onclick={() => doRequestElevation()}
        disabled={elevLoading}
        class="px-4 py-2 rounded-md bg-accent text-paper-soft text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {elevLoading ? 'Requesting…' : 'Request elevation'}
      </button>
      {#if elevError}
        <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{elevError}</div>
      {/if}
      {#if elevNotice}
        <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">{elevNotice}</div>
      {/if}
    </div>

    {#if elevations.length > 0}
      <div class="mt-4 rounded-md border border-rule overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Grant</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Role</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
              <th class="px-4 py-2.5 text-right font-medium text-ink-mute text-xs uppercase tracking-wider">Action</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each elevations as grant (grant.id)}
              <tr class="hover:bg-paper-soft transition-colors">
                <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{grant.id}</td>
                <td class="px-4 py-2.5 text-xs text-ink font-medium">{grant.role}</td>
                <td class="px-4 py-2.5">
                  {#if grant.approved}
                    <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success-soft text-success">Approved</span>
                  {:else}
                    <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-accent-soft text-accent-deep">Pending</span>
                  {/if}
                </td>
                <td class="px-4 py-2.5 text-right">
                  {#if !grant.approved}
                    <button
                      onclick={() => doApproveElevation(grant)}
                      class="text-sm text-accent-deep hover:underline"
                    >Approve</button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="mt-4">
        <EmptyState message="No elevation grants this session." hint="Requests made here appear with their grant id until the page is reloaded." />
      </div>
    {/if}
  </section>

  <!-- Region kill switch -->
  <section aria-labelledby="region-heading">
    <h2 id="region-heading" class="text-lg font-display font-semibold text-ink mb-3">Region kill switch</h2>
    <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-3 max-w-xl">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Region</span>
          <input
            type="text"
            bind:value={regionName}
            placeholder="e.g. eu-central"
            class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40"
          />
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">State</span>
          <select bind:value={regionState} class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink focus:outline-none focus:ring-2 focus:ring-focus-ring/40">
            <option value="active">active</option>
            <option value="draining">draining</option>
            <option value="disabled">disabled</option>
          </select>
        </label>
      </div>
      <button
        onclick={() => doSetRegionState()}
        disabled={regionLoading}
        class="px-4 py-2 rounded-md bg-marine text-paper-soft text-sm font-medium hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {regionLoading ? 'Applying…' : 'Apply state'}
      </button>
      {#if regionError}
        <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{regionError}</div>
      {/if}
      {#if regionNotice}
        <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">{regionNotice}</div>
      {/if}
      <p class="text-xs text-ink-mute">
        Draining stops new job placement and finishes in-flight work; disabled rejects dispatch entirely.
      </p>
    </div>
  </section>
</div>
