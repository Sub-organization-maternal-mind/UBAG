<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  // SCIM user shape
  interface ScimName { formatted?: string; givenName?: string; familyName?: string; }
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

  let groups = $state<ScimGroup[]>([]);
  let groupsLoading = $state(true);
  let groupsDenied = $state(false);
  let groupsError = $state<string | null>(null);

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
    const res = await api.get<ScimUserList>('/v1/scim/users');
    usersLoading = false;
    if (res.denied) { usersDenied = true; return; }
    if (res.error && res.status !== 404) { usersError = res.error; return; }
    users = res.data?.Resources ?? [];
  }

  async function loadGroups() {
    groupsLoading = true;
    groupsError = null;
    groupsDenied = false;
    const res = await api.get<ScimGroupList>('/v1/scim/groups');
    groupsLoading = false;
    if (res.denied) { groupsDenied = true; return; }
    if (res.error && res.status !== 404) { groupsError = res.error; return; }
    groups = res.data?.Resources ?? [];
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
  });
</script>

<div class="space-y-8">
  <h1 class="text-2xl font-display font-bold text-ink">Users &amp; Roles</h1>

  <!-- Users Section -->
  <section aria-labelledby="users-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="users-heading" class="text-lg font-display font-semibold text-ink">Users</h2>
      <button onclick={() => loadUsers()} class="text-sm text-accent-deep hover:underline">Refresh</button>
    </div>

    {#if usersLoading}
      <div class="text-ink-mute text-sm">Loading users…</div>
    {:else if usersDenied}
      <DeniedPanel resource="SCIM users" />
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
      <button onclick={() => loadGroups()} class="text-sm text-accent-deep hover:underline">Refresh</button>
    </div>

    {#if groupsLoading}
      <div class="text-ink-mute text-sm">Loading groups…</div>
    {:else if groupsDenied}
      <DeniedPanel resource="SCIM groups" />
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

  <!-- RBAC Roles Section -->
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
      Set your App Secret on the <a href="/settings" class="text-accent-deep hover:underline">Settings</a> page.
    </p>
  </section>
</div>
