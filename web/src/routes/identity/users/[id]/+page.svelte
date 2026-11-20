<script lang="ts">
	import { page } from '$app/state';
	import { api, type Activation, type AdminUser, type AuditEvent, type Group, type Role, type Usage } from '#lib/api.ts';
	import { can } from '#lib/permissions.ts';
	import { dateTime, formatBytes, timeAgo } from '#lib/format.ts';
	import ActivationLink from '#lib/components/ActivationLink.svelte';
	import ActivityList from '#lib/components/ActivityList.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	let user = $state<AdminUser | null>(null);
	let groups = $state<Group[]>([]);
	let roles = $state<Role[]>([]);
	let roleToGive = $state('');
	let events = $state<AuditEvent[]>([]);
	let usage = $state<Usage | null>(null);
	let quotaGB = $state('');
	let error = $state<unknown>(null);
	let saved = $state(false);
	let activation = $state<Activation | null>(null);
	let busy = $state(false);

	let displayName = $state('');
	let email = $state('');

	const id = $derived(page.params.id ?? '');
	const self = $derived(user?.id === data.user?.id);
	const memberOf = $derived(new Set(user?.groups.map((g) => g.id)));
	// Each part of the page follows its own permission (ADR 0022).
	const may = (p: string) => can(data.user, p);
	// Roles this administrator may give: only those within their own permissions.
	const givable = $derived(
		roles.filter((r) => r.permissions.every((p) => may(p)) && !user?.roles.some((x) => x.id === r.id && x.group === null))
	);

	async function load() {
		try {
			[user, groups, events, usage, roles] = await Promise.all([
				api.admin.user(id),
				may('identity.groups.view') ? api.admin.groups() : Promise.resolve([]),
				api.admin.userActivity(id),
				may('drive.quota.manage') ? api.drive.userUsage(id) : Promise.resolve(null),
				may('identity.roles.assign') ? api.admin.roles() : Promise.resolve([])
			]);
			quotaGB = usage?.limit_bytes == null ? '' : String(usage.limit_bytes / 1024 ** 3);
			displayName = user.display_name;
			email = user.email;
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
			await load();
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	function save(e: SubmitEvent) {
		e.preventDefault();
		if (!user) return;
		const changes: { display_name?: string; email?: string } = {};
		if (displayName !== user.display_name) changes.display_name = displayName;
		if (email.trim() !== user.email) changes.email = email.trim();
		return act(async () => {
			await api.admin.updateUser(id, changes);
			saved = true;
		});
	}

	const reset = () =>
		confirm('Wachtwoord, authenticator-app, passkeys en herstelcodes van dit account vervallen en alle sessies worden beëindigd. Doorgaan?') &&
		act(async () => {
			activation = await api.admin.resetUser(id);
		});

	const toggleDisabled = () => {
		const disable = user?.status !== 'disabled';
		if (disable && !confirm('Account uitschakelen? De gebruiker wordt direct overal uitgelogd.')) return;
		return act(() => api.admin.setDisabled(id, disable));
	};

	function saveQuota(e: SubmitEvent) {
		e.preventDefault();
		const gb = quotaGB.trim() === '' ? null : Number(quotaGB.replace(',', '.'));
		if (gb !== null && (!Number.isFinite(gb) || gb < 0)) {
			error = new Error('Vul een aantal GB in, of laat het veld leeg voor geen limiet.');
			return;
		}
		return act(async () => {
			usage = await api.drive.setQuota(id, gb === null ? null : Math.round(gb * 1024 ** 3));
			saved = true;
		});
	}

	const toggleGroup = (groupId: string, member: boolean) => act(() => api.admin.setMember(groupId, id, member));

	function giveRole(e: SubmitEvent) {
		e.preventDefault();
		const role = roleToGive;
		if (!role) return;
		return act(async () => {
			await api.admin.setRole(role, 'users', id, true);
			roleToGive = '';
		});
	}

	const takeRole = (roleId: string, name: string) =>
		confirm(`De rol "${name}" intrekken?`) && act(() => api.admin.setRole(roleId, 'users', id, false));
</script>

<svelte:head>
	<title>{user?.display_name ?? 'Gebruiker'} · Identity · Cloud</title>
</svelte:head>

<p><a href="/identity/users">← Gebruikers</a></p>

<ErrorNotice {error} />
{#if activation && user}<ActivationLink {activation} username={user.username} email={user.email} />{/if}

{#if user}
	<div class="page-header">
		<h1>{user.display_name}</h1>
		<span class="badge" class:badge--ok={user.status === 'active'} class:badge--error={user.status === 'disabled'}>
			{user.status === 'active' ? 'Actief' : user.status === 'pending' ? 'Wacht op activatie' : 'Uitgeschakeld'}
		</span>
	</div>

	<div class="columns">
		<div class="stack">
			<form class="panel form" onsubmit={save}>
				<h2>Gegevens</h2>
				{#if saved}<p class="flash flash--success" role="status">Opgeslagen.</p>{/if}
				<dl class="details">
					<dt>Gebruikersnaam</dt>
					<dd><code>{user.username}</code></dd>
					<dt>Aangemaakt</dt>
					<dd><time datetime={user.created_at} title={dateTime(user.created_at)}>{timeAgo(user.created_at)}</time></dd>
				</dl>
				<label class="field">
					<span>Naam</span>
					<input bind:value={displayName} required maxlength="100" readonly={!may('identity.users.edit')} />
				</label>
				<label class="field">
					<span>E-mailadres</span>
					<input type="email" bind:value={email} maxlength="254" placeholder="Geen" readonly={!may('identity.users.edit')} />
				</label>
				{#if may('identity.users.edit')}<div><button class="button button--primary" disabled={busy}>Opslaan</button></div>{/if}
			</form>

			<section class="panel">
				<h2>Inloggen</h2>
				<dl class="details">
					<dt>Authenticator-app</dt>
					<dd>{user.has_totp ? 'Gekoppeld' : 'Nog niet gekoppeld'}</dd>
					<dt>Passkeys</dt>
					<dd>{user.passkeys}</dd>
					<dt>Herstelcodes</dt>
					<dd>{user.recovery_codes} over</dd>
					<dt>Laatst ingelogd</dt>
					<dd>
						{#if user.last_login_at}<time datetime={user.last_login_at} title={dateTime(user.last_login_at)}>{timeAgo(user.last_login_at)}</time>{:else}Nooit{/if}
					</dd>
				</dl>
				{#if self}
					<p class="muted small">Dit is je eigen account. Beheer je inloggegevens via <a href="/account">Account</a>.</p>
				{:else}
					<div class="actions">
						{#if may('identity.users.reset')}
							<button class="button" onclick={reset} disabled={busy || user.status === 'disabled'}>Account resetten</button>
						{/if}
						{#if may('identity.users.disable')}
							<button class="button" onclick={toggleDisabled} disabled={busy}>
								{user.status === 'disabled' ? 'Inschakelen' : 'Uitschakelen'}
							</button>
						{/if}
					</div>
					<p class="muted small">Resetten geeft een nieuwe activatielink, bijvoorbeeld als iemand zijn telefoon kwijt is.</p>
				{/if}
			</section>

			<section class="panel">
				<h2>Rollen</h2>
				{#if user.roles.length === 0}
					<p class="muted">Geen beheerrechten.</p>
				{:else}
					<ul class="list">
						{#each user.roles as r (r.id + (r.group ?? ''))}
							<li>
								<span>{r.name}{#if r.group}<span class="muted small"> · via groep {r.group}</span>{/if}</span>
								{#if !r.group && may('identity.roles.assign')}
									<button class="button" disabled={busy} onclick={() => takeRole(r.id, r.name)}>Intrekken</button>
								{/if}
							</li>
						{/each}
					</ul>
				{/if}
				{#if may('identity.roles.assign') && givable.length > 0}
					<form class="add" onsubmit={giveRole}>
						<select bind:value={roleToGive} aria-label="Rol toekennen">
							<option value="">Rol kiezen…</option>
							{#each givable as r (r.id)}<option value={r.id}>{r.name}</option>{/each}
						</select>
						<button class="button" disabled={busy || !roleToGive}>Toekennen</button>
					</form>
					<p class="muted small">Je kunt alleen rollen geven waarvan je alle rechten zelf hebt.</p>
				{/if}
			</section>

			{#if usage}
			<section class="panel">
				<h2>Opslag</h2>
				{#if usage}
					<p>
						{formatBytes(usage.used_bytes)} gebruikt{#if usage.limit_bytes !== null}&nbsp;van {formatBytes(usage.limit_bytes)}{/if}
					</p>
				{/if}
				<form class="add" onsubmit={saveQuota}>
					<input bind:value={quotaGB} inputmode="decimal" placeholder="Geen limiet" aria-label="Limiet in GB" />
					<span class="muted">GB</span>
					<button class="button" disabled={busy}>Limiet opslaan</button>
				</form>
				<p class="muted small">Laat leeg voor geen limiet. Ook bestanden in de prullenbak en oude versies tellen mee.</p>
			</section>
			{/if}

			<section class="panel">
				<h2>Groepen</h2>
				{#if !may('identity.groups.view')}
					<p>{user.groups.map((g) => g.name).join(', ') || 'Geen'}</p>
				{:else if groups.length === 0}
					<p class="muted">Er zijn nog geen groepen. <a href="/identity/groups">Groep aanmaken</a></p>
				{:else}
					{#each groups as g (g.id)}
						<label class="check">
							<input type="checkbox" checked={memberOf.has(g.id)} disabled={busy || !may('identity.groups.manage')} onchange={(e) => toggleGroup(g.id, e.currentTarget.checked)} />
							{g.name}
							{#if g.description}<span class="muted small">{g.description}</span>{/if}
						</label>
					{/each}
				{/if}
			</section>
		</div>

		<section class="panel">
			<h2>Activiteit</h2>
			<ActivityList {events} />
		</section>
	</div>
{/if}

<style>
	.page-header {
		display: flex;
		align-items: center;
		gap: 1rem;
		margin-bottom: 1.5rem;
	}
	.page-header h1 {
		margin: 0;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.check + .check {
		margin-top: 0.4rem;
	}
</style>
