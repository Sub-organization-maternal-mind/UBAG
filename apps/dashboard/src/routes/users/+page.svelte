<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { api } from '$lib/api/client';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  // SCIM user shape
  interface ScimEmail { value: string; primary?: boolean; }
  interface ScimUser {
    id: string;
    userName: string;
    displayName?: string;
    emails?: ScimEmail[];
    active?: boolean;
    groups?: Array<{ value: string; display?: string }>;
  }
  interface ScimUserList { totalResults?: number; Resources?: ScimUser[]; itemsPerPage?: number; }

  interface ScimMember { value: string; display?: string; }
  interface ScimGroup {
    id: string;
    displayName: string;
    members?: ScimMember[];
  }
  interface ScimGroupList { totalResults?: number; Resources?: ScimGroup[]; }

  interface Role { name: string; description: string; }

  // State — per-section independent
  let users = $state<ScimUser[]>([]);
  let usersLoading = $state(true);
  let usersDenied = $state(false);
  let usersError = $state<string | null>(null);
  // SCIM 404 = not implemented on this deployment
  let scimUsersUnavailable = $state(false);

  let groups = $state<ScimGroup[]>([]);
  let groupsLoading = $state(true);
  let groupsDenied = $state(false);
  let groupsError = $state<string | null>(null);
  let scimGroupsUnavailable = $state(false);

  const ROLES: Role[] = [
    { name: 'viewer',    description: 'Read-only access to jobs, targets, and status endpoints.' },
    { name: 'service',   description: 'Can dispatch jobs and read results. No admin access.' },
    { name: 'developer', description: 'Full job and target management. Cannot manage users or billing.' },
    { name: 'operator',  description: 'Full gateway management including targets, adapters, and workflows.' },
    { name: 'admin',     description: 'Unrestricted access including SCIM, billing, audit, and settings.' },
  ];

  async function loadUsers() {
    usersLoading = true;
    usersError = null;
    usersDenied = false;
    scimUsersUnavailable = false;
    const res = await api.get<ScimUserList>('/v1/scim/users');
    usersLoading = false;
    if (res.denied) { usersDenied = true; return; }
    // 404 means SCIM is not implemented on this deployment — show a friendly message
    if (res.status === 404 || (res.error && res.status !== 200)) {
      scimUsersUnavailable = true;
      return;
    }
    users = res.data?.Resources ?? [];
  }

  async function loadGroups() {
    groupsLoading = true;
    groupsError = null;
    groupsDenied = false;
    scimGroupsUnavailable = false;
    const res = await api.get<ScimGroupList>('/v1/scim/groups');
    groupsLoading = false;
    if (res.denied) { groupsDenied = true; return; }
    // 404 means SCIM is not implemented on this deployment
    if (res.status === 404 || (res.error && res.status !== 200)) {
      scimGroupsUnavailable = true;
      return;
    }
    groups = res.data?.Resources ?? [];
  }

  // --- Create user dialog (POST /v1/scim/users) ---
  let createUserOpen = $state(false);
  let newUserName = $state('');
  let newDisplayName = $state('');
  let newEmail = $state('');
  let createUserLoading = $state(false);
  let createUserError = $state<string | null>(null);

  async function doCreateUser() {
    const userName = newUserName.trim();
    if (!userName) {
      createUserError = 'Username is required.';
      return;
    }
    createUserLoading = true;
    createUserError = null;
    const res = await api.post<ScimUser>('/v1/scim/users', {
      userName,
      displayName: newDisplayName.trim() || userName,
      emails: newEmail.trim() ? [{ value: newEmail.trim(), primary: true }] : [],
      active: true,
    });
    createUserLoading = false;
    if (res.error) {
      createUserError = `Create failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    createUserOpen = false;
    newUserName = '';
    newDisplayName = '';
    newEmail = '';
    await loadUsers();
  }

  // --- Create group dialog (POST /v1/scim/groups) ---
  let createGroupOpen = $state(false);
  let newGroupName = $state('');
  let createGroupLoading = $state(false);
  let createGroupError = $state<string | null>(null);

  async function doCreateGroup() {
    const displayName = newGroupName.trim();
    if (!displayName) {
      createGroupError = 'Display name is required.';
      return;
    }
    createGroupLoading = true;
    createGroupError = null;
    const res = await api.post<ScimGroup>('/v1/scim/groups', { displayName });
    createGroupLoading = false;
    if (res.error) {
      createGroupError = `Create failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    createGroupOpen = false;
    newGroupName = '';
    await loadGroups();
  }

  // --- SSO status (GET /v1/sso/config, read-only) ---
  interface SsoProvider {
    issuer?: string; entity_id?: string; client_id?: string;
    [key: string]: unknown;
  }
  let ssoLoading = $state(true);
  let ssoDenied = $state(false);
  let ssoError = $state<string | null>(null);
  let ssoUnavailable = $state(false);
  let ssoOidc = $state<SsoProvider[]>([]);
  let ssoSaml = $state<SsoProvider[]>([]);

  async function loadSso() {
    ssoLoading = true;
    ssoError = null;
    ssoDenied = false;
    ssoUnavailable = false;
    const res = await api.get<{ oidc?: SsoProvider[]; saml?: SsoProvider[] }>('/v1/sso/config');
    ssoLoading = false;
    if (res.denied) { ssoDenied = true; return; }
    if (res.status === 404 || res.status === 501 || (res.error && res.status !== 200)) {
      ssoUnavailable = true;
      return;
    }
    ssoOidc = res.data?.oidc ?? [];
    ssoSaml = res.data?.saml ?? [];
  }

  function ssoLabel(p: SsoProvider): string {
    const v = p.issuer ?? p.entity_id ?? p.client_id;
    return typeof v === 'string' && v ? v : '(configured)';
  }

  // --- MFA enrollment (POST /v1/mfa/enroll) ---
  interface MfaEnrollment {
    secret?: string; otpauth_uri?: string; otpauth_url?: string; recovery_codes?: string[];
  }
  let mfaLoading = $state(false);
  let mfaError = $state<string | null>(null);
  let mfaEnrollment = $state<MfaEnrollment | null>(null);

  async function doEnrollMfa() {
    mfaLoading = true;
    mfaError = null;
    mfaEnrollment = null;
    const res = await api.post<MfaEnrollment>('/v1/mfa/enroll', { issuer: 'UBAG' });
    mfaLoading = false;
    if (res.error) {
      mfaError = res.status === 501
        ? 'MFA is not enabled on this gateway.'
        : `Enrollment failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    mfaEnrollment = (res.data as MfaEnrollment | null) ?? null;
  }

  function primaryEmail(u: ScimUser): string {
    if (!u.emails?.length) return '—';
    const primary = u.emails.find(e => e.primary);
    return (primary ?? u.emails[0]).value;
  }

  function userGroups(u: ScimUser): string {
    if (!u.groups?.length) return '—';
    return u.groups.map(g => g.display ?? g.value).join(', ');
  }

  onMount(() => {
    loadUsers();
    loadGroups();
    loadSso();
  });
</script>

<div class="space-y-8">
  <h1 class="text-2xl font-display font-bold text-ink">Users &amp; Roles</h1>

  <!-- Users Section -->
  <section aria-labelledby="users-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="users-heading" class="text-lg font-display font-semibold text-ink">Users</h2>
      <div class="flex items-center gap-3">
        {#if !scimUsersUnavailable}
          <button onclick={() => { createUserOpen = true; createUserError = null; }} class="text-sm text-accent-deep hover:underline">Create user</button>
        {/if}
        <button onclick={() => loadUsers()} class="text-sm text-accent-deep hover:underline">Refresh</button>
      </div>
    </div>

    {#if usersLoading}
      <div class="text-ink-mute text-sm">Loading users…</div>
    {:else if usersDenied}
      <DeniedPanel resource="SCIM users" />
    {:else if scimUsersUnavailable}
      <EmptyState
        message="SCIM user provisioning is not enabled on this deployment."
        hint="The gateway does not expose /v1/scim/users. Contact your administrator to enable SCIM."
      />
    {:else if usersError}
      <ErrorPanel message={usersError} retry={loadUsers} />
    {:else if users.length === 0}
      <EmptyState message="No users found." hint="SCIM provisioning may not be configured." />
    {:else}
      <div class="rounded-md border border-rule overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Username</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Email</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Active</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Groups</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each users as user (user.id)}
              <tr class="hover:bg-paper-soft transition-colors">
                <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{user.id}</td>
                <td class="px-4 py-2.5 text-ink font-medium text-xs">{user.userName}</td>
                <td class="px-4 py-2.5 text-ink-soft text-xs">{primaryEmail(user)}</td>
                <td class="px-4 py-2.5">
                  {#if user.active === false}
                    <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-danger-soft text-danger">Inactive</span>
                  {:else}
                    <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success-soft text-success">Active</span>
                  {/if}
                </td>
                <td class="px-4 py-2.5 text-ink-soft text-xs">{userGroups(user)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <!-- Groups Section -->
  <section aria-labelledby="groups-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="groups-heading" class="text-lg font-display font-semibold text-ink">Groups</h2>
      <div class="flex items-center gap-3">
        {#if !scimGroupsUnavailable}
          <button onclick={() => { createGroupOpen = true; createGroupError = null; }} class="text-sm text-accent-deep hover:underline">Create group</button>
        {/if}
        <button onclick={() => loadGroups()} class="text-sm text-accent-deep hover:underline">Refresh</button>
      </div>
    </div>

    {#if groupsLoading}
      <div class="text-ink-mute text-sm">Loading groups…</div>
    {:else if groupsDenied}
      <DeniedPanel resource="SCIM groups" />
    {:else if scimGroupsUnavailable}
      <EmptyState
        message="SCIM group provisioning is not enabled on this deployment."
        hint="The gateway does not expose /v1/scim/groups. Contact your administrator to enable SCIM."
      />
    {:else if groupsError}
      <ErrorPanel message={groupsError} retry={loadGroups} />
    {:else if groups.length === 0}
      <EmptyState message="No groups found." hint="SCIM provisioning may not be configured." />
    {:else}
      <div class="rounded-md border border-rule overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Display Name</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Member Count</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each groups as group (group.id)}
              <tr class="hover:bg-paper-soft transition-colors">
                <td class="px-4 py-2.5 font-mono text-xs text-ink-mute">{group.id}</td>
                <td class="px-4 py-2.5 text-ink font-medium text-xs">{group.displayName}</td>
                <td class="px-4 py-2.5 text-ink-soft text-xs">{group.members?.length ?? 0}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <!-- RBAC Roles Section (always visible) -->
  <section aria-labelledby="roles-heading">
    <h2 id="roles-heading" class="text-lg font-display font-semibold text-ink mb-3">RBAC Roles</h2>
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {#each ROLES as role}
        <div class="rounded-md border border-rule bg-paper-soft p-4">
          <p class="font-mono font-semibold text-accent-deep text-sm">{role.name}</p>
          <p class="text-xs text-ink-soft mt-1">{role.description}</p>
        </div>
      {/each}
    </div>
    <p class="text-xs text-ink-mute mt-3">
      Role is determined by the <code class="font-mono">UBAG_ACTOR_ROLE</code> environment variable on the gateway.
      Set your App Secret on the <a href="{base}/settings" class="text-accent-deep hover:underline">Settings</a> page.
    </p>
  </section>

  <!-- SSO Section -->
  <section aria-labelledby="sso-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="sso-heading" class="text-lg font-display font-semibold text-ink">Single Sign-On</h2>
      <button onclick={() => loadSso()} class="text-sm text-accent-deep hover:underline">Refresh</button>
    </div>
    {#if ssoLoading}
      <div class="text-ink-mute text-sm">Loading…</div>
    {:else if ssoDenied}
      <DeniedPanel resource="SSO configuration" />
    {:else if ssoUnavailable}
      <EmptyState
        message="Single sign-on is not configured on this deployment."
        hint="An operator can configure OIDC or SAML providers on the gateway."
      />
    {:else if ssoError}
      <ErrorPanel message={ssoError} retry={loadSso} />
    {:else if ssoOidc.length === 0 && ssoSaml.length === 0}
      <EmptyState message="No identity providers configured." hint="An operator can configure OIDC or SAML providers on the gateway." />
    {:else}
      <div class="grid gap-3 sm:grid-cols-2">
        {#if ssoOidc.length > 0}
          <div class="rounded-md border border-rule bg-paper-soft p-4">
            <p class="font-mono font-semibold text-accent-deep text-sm">OIDC</p>
            <ul class="mt-2 space-y-1">
              {#each ssoOidc as p, i (i)}
                <li class="text-xs text-ink-soft font-mono">{ssoLabel(p)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if ssoSaml.length > 0}
          <div class="rounded-md border border-rule bg-paper-soft p-4">
            <p class="font-mono font-semibold text-accent-deep text-sm">SAML</p>
            <ul class="mt-2 space-y-1">
              {#each ssoSaml as p, i (i)}
                <li class="text-xs text-ink-soft font-mono">{ssoLabel(p)}</li>
              {/each}
            </ul>
          </div>
        {/if}
      </div>
    {/if}
  </section>

  <!-- MFA Enrollment Section -->
  <section aria-labelledby="mfa-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="mfa-heading" class="text-lg font-display font-semibold text-ink">Multi-Factor Authentication</h2>
      <button
        onclick={() => doEnrollMfa()}
        disabled={mfaLoading}
        class="text-sm text-accent-deep hover:underline disabled:opacity-40 disabled:cursor-not-allowed"
      >
        {mfaLoading ? 'Enrolling…' : mfaEnrollment ? 'Re-enroll' : 'Enroll this identity'}
      </button>
    </div>
    {#if mfaError}
      <ErrorPanel message={mfaError} retry={doEnrollMfa} />
    {:else if mfaEnrollment}
      <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-3">
        <p class="text-sm text-ink-soft">
          Scan the provisioning URI with your authenticator app, then store the recovery codes somewhere safe —
          they are shown <strong>once</strong> and never again.
        </p>
        {#if mfaEnrollment.otpauth_uri ?? mfaEnrollment.otpauth_url}
          <div>
            <p class="text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Provisioning URI</p>
            <code class="block text-xs font-mono text-ink bg-paper border border-rule rounded-md px-3 py-2 break-all select-all">{mfaEnrollment.otpauth_uri ?? mfaEnrollment.otpauth_url}</code>
          </div>
        {/if}
        {#if mfaEnrollment.secret}
          <div>
            <p class="text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Manual entry secret</p>
            <code class="block text-xs font-mono text-ink bg-paper border border-rule rounded-md px-3 py-2 break-all select-all">{mfaEnrollment.secret}</code>
          </div>
        {/if}
        {#if mfaEnrollment.recovery_codes?.length}
          <div>
            <p class="text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Recovery codes</p>
            <ul class="grid gap-1 sm:grid-cols-2">
              {#each mfaEnrollment.recovery_codes as code, i (i)}
                <li><code class="block text-xs font-mono text-ink bg-paper border border-rule rounded-md px-3 py-1.5 select-all">{code}</code></li>
              {/each}
            </ul>
          </div>
        {/if}
      </div>
    {:else}
      <p class="text-sm text-ink-mute">Enroll this identity in TOTP multi-factor authentication. Requires MFA to be enabled on the gateway.</p>
    {/if}
  </section>
</div>

<!-- Create user dialog -->
{#if createUserOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-ink/40 p-4" role="presentation" onclick={() => { createUserOpen = false; }}>
    <div
      class="w-full max-w-md rounded-lg border border-rule bg-paper shadow-2xl"
      role="dialog"
      aria-modal="true"
      aria-label="Create user"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => { if (e.key === 'Escape') createUserOpen = false; }}
      tabindex="-1"
    >
      <div class="px-5 py-4 border-b border-rule bg-paper-soft">
        <h2 class="text-lg font-display font-semibold text-ink">Create User</h2>
      </div>
      <div class="p-5 space-y-3">
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Username *</span>
          <input type="text" bind:value={newUserName} placeholder="e.g. j.operator" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Display name</span>
          <input type="text" bind:value={newDisplayName} placeholder="e.g. J. Operator" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Email</span>
          <input type="email" bind:value={newEmail} placeholder="e.g. j.operator@example.com" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
        {#if createUserError}
          <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{createUserError}</div>
        {/if}
      </div>
      <div class="px-5 py-3 border-t border-rule flex justify-end gap-3">
        <button onclick={() => { createUserOpen = false; }} class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-paper-warm transition-colors">Cancel</button>
        <button onclick={() => doCreateUser()} disabled={createUserLoading} class="px-4 py-2 rounded-md bg-accent text-paper-soft text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors">{createUserLoading ? 'Creating…' : 'Create'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Create group dialog -->
{#if createGroupOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-ink/40 p-4" role="presentation" onclick={() => { createGroupOpen = false; }}>
    <div
      class="w-full max-w-md rounded-lg border border-rule bg-paper shadow-2xl"
      role="dialog"
      aria-modal="true"
      aria-label="Create group"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => { if (e.key === 'Escape') createGroupOpen = false; }}
      tabindex="-1"
    >
      <div class="px-5 py-4 border-b border-rule bg-paper-soft">
        <h2 class="text-lg font-display font-semibold text-ink">Create Group</h2>
      </div>
      <div class="p-5 space-y-3">
        <label class="block">
          <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Display name *</span>
          <input type="text" bind:value={newGroupName} placeholder="e.g. on-call" class="w-full px-3 py-1.5 rounded-md border border-rule bg-paper text-sm text-ink placeholder:text-ink-mute focus:outline-none focus:ring-2 focus:ring-focus-ring/40" />
        </label>
        {#if createGroupError}
          <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{createGroupError}</div>
        {/if}
      </div>
      <div class="px-5 py-3 border-t border-rule flex justify-end gap-3">
        <button onclick={() => { createGroupOpen = false; }} class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-paper-warm transition-colors">Cancel</button>
        <button onclick={() => doCreateGroup()} disabled={createGroupLoading} class="px-4 py-2 rounded-md bg-accent text-paper-soft text-sm font-medium hover:bg-accent-deep disabled:opacity-40 disabled:cursor-not-allowed transition-colors">{createGroupLoading ? 'Creating…' : 'Create'}</button>
      </div>
    </div>
  </div>
{/if}
