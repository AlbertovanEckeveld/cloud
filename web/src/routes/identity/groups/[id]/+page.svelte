<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type AdminUser, type GroupDetail, type Role } from '#lib/api.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import { can } from '#lib/permissions.ts';

	let group = $state<GroupDetail | null>(null);
	let users = $state<AdminUser[]>([]);
	let error = $state<unknown>(null);
	let saved = $state(false);
	let busy = $state(false);
	let name = $state('');
	let description = $state('');
	let newMember = $state('');
	let roles = $state<Role[]>([]);
	let roleToGive = $state('');

	const id = $derived(page.params.id ?? '');
	const candidates = $derived(users.filter((u) => !group?.members.some((m) => m.id === u.id)));
	const may = (p: string) => can(page.data.user, p);
	const mayManage = $derived(may('identity.groups.manage'));
	// Roles this administrator may give: only those within their own permissions.
	const givable = $derived(roles.filter((r) => r.permissions.every((p) => may(p)) && !group?.roles.some((x) => x.id === r.id)));

	async function load() {
		try {
			[group, users, roles] = await Promise.all([
				api.admin.group(id),
				mayManage && may('identity.users.view') ? api.admin.users() : Promise.resolve([]),
				may('identity.roles.assign') ? api.admin.roles() : Promise.resolve([])
			]);
			name = group.name;
			description = group.description;
		} catch (e) {
			error = e;
		}
	}

	$effect(() => {
		if (id) load();
	});

	async function act(action: () => Promise<void>) {
		busy = true;
		error = null;
		saved = false;
		try {
			await action();
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	function save(e: SubmitEvent) {
		e.preventDefault();
		return act(async () => {
			await api.admin.updateGroup(id, name, description);
			saved = true;
			await load();
		});
	}

	function addMember(e: SubmitEvent) {
		e.preventDefault();
		return act(async () => {
			await api.admin.setMember(id, newMember, true);
			newMember = '';
			await load();
		});
	}

	const removeMember = (userId: string) =>
		act(async () => {
			await api.admin.setMember(id, userId, false);
			await load();
		});

	function giveRole(e: SubmitEvent) {
		e.preventDefault();
		const role = roleToGive;
		if (!role) return;
		return act(async () => {
			await api.admin.setRole(role, 'groups', id, true);
			roleToGive = '';
			await load();
		});
	}

	const takeRole = (roleId: string, name: string) =>
		confirm(`De rol "${name}" intrekken bij deze groep? Alle leden verliezen de rechten direct.`) &&
		act(async () => {
			await api.admin.setRole(roleId, 'groups', id, false);
			await load();
		});

	const remove = () =>
		confirm(`Groep "${group?.name}" verwijderen? De leden blijven bestaan.`) &&
		act(async () => {
			await api.admin.deleteGroup(id);
			await goto('/identity/groups');
		});
</script>

<svelte:head>
	<title>{group?.name ?? 'Groep'} · Identity · Cloud</title>
</svelte:head>

<p><a href="/identity/groups">← Groepen</a></p>
<ErrorNotice {error} />

{#if group}
	<h1>{group.name}</h1>
	<div class="columns">
		<section class="panel">
			<h2>Leden</h2>
			{#if group.members.length > 0}
				<ul class="list">
					{#each group.members as m (m.id)}
						<li>
							<div>
								<a class="list__title" href="/identity/users/{m.id}">{m.display_name}</a>
								<code>{m.username}</code>
							</div>
							{#if mayManage}<button class="button" onclick={() => removeMember(m.id)} disabled={busy}>Verwijderen</button>{/if}
						</li>
					{/each}
				</ul>
			{:else}
				<p class="muted">Deze groep heeft nog geen leden.</p>
			{/if}
			{#if mayManage && candidates.length > 0}
				<form class="add" onsubmit={addMember}>
					<select bind:value={newMember} required aria-label="Gebruiker toevoegen">
						<option value="" disabled>Kies een gebruiker…</option>
						{#each candidates as u (u.id)}
							<option value={u.id}>{u.display_name} ({u.username})</option>
						{/each}
					</select>
					<button class="button button--primary" disabled={busy || !newMember}>Toevoegen</button>
				</form>
			{/if}
		</section>

		<div class="stack">
			<section class="panel">
				<h2>Rollen</h2>
				{#if group.roles.length === 0}
					<p class="muted">Deze groep geeft geen beheerrechten.</p>
				{:else}
					<p class="flash">Iedereen in deze groep heeft deze rollen. Leden toevoegen of verwijderen is dus rechten uitdelen of afnemen.</p>
					<ul class="list">
						{#each group.roles as r (r.id)}
							<li>
								<span>{r.name}</span>
								{#if may('identity.roles.assign')}<button class="button" disabled={busy} onclick={() => takeRole(r.id, r.name)}>Intrekken</button>{/if}
							</li>
						{/each}
					</ul>
				{/if}
				{#if may('identity.roles.assign') && givable.length > 0}
					<form class="add" onsubmit={giveRole}>
						<select bind:value={roleToGive} aria-label="Rol toekennen aan deze groep">
							<option value="">Rol kiezen…</option>
							{#each givable as r (r.id)}<option value={r.id}>{r.name}</option>{/each}
						</select>
						<button class="button" disabled={busy || !roleToGive}>Toekennen</button>
					</form>
				{/if}
			</section>
			{#if mayManage}
			<form class="panel form" onsubmit={save}>
				<h2>Instellingen</h2>
				{#if saved}<p class="flash flash--success" role="status">Opgeslagen.</p>{/if}
				<label class="field">
					<span>Naam</span>
					<input bind:value={name} required maxlength="32" autocapitalize="none" />
					<p class="field__hint">Let op: apps die rechten aan deze naam koppelen, moeten mee veranderen.</p>
				</label>
				<label class="field">
					<span>Omschrijving</span>
					<input bind:value={description} maxlength="200" />
				</label>
				<div><button class="button button--primary" disabled={busy}>Opslaan</button></div>
			</form>
			<section class="panel">
				<h2>Groep verwijderen</h2>
				<p class="muted small">De leden blijven bestaan; alleen de groep verdwijnt.</p>
				<button class="button" onclick={remove} disabled={busy}>Verwijderen</button>
			</section>
			{/if}
		</div>
	</div>
{/if}
