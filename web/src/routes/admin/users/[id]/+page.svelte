<script lang="ts">
	import { page } from '$app/state';
	import { api, type AdminUser, type AuditEvent, type Group } from '#lib/api.ts';
	import { dateTime, timeAgo } from '#lib/format.ts';
	import ActivationLink from '#lib/components/ActivationLink.svelte';
	import ActivityList from '#lib/components/ActivityList.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	let user = $state<AdminUser | null>(null);
	let groups = $state<Group[]>([]);
	let events = $state<AuditEvent[]>([]);
	let error = $state<unknown>(null);
	let saved = $state(false);
	let link = $state<string | null>(null);
	let busy = $state(false);

	let displayName = $state('');
	let isAdmin = $state(false);

	const id = $derived(page.params.id ?? '');
	const self = $derived(user?.id === data.user?.id);
	const memberOf = $derived(new Set(user?.groups.map((g) => g.id)));

	async function load() {
		try {
			[user, groups, events] = await Promise.all([api.admin.user(id), api.admin.groups(), api.admin.userActivity(id)]);
			displayName = user.display_name;
			isAdmin = user.is_admin;
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
		const changes: { display_name?: string; is_admin?: boolean } = {};
		if (displayName !== user.display_name) changes.display_name = displayName;
		if (isAdmin !== user.is_admin) changes.is_admin = isAdmin;
		return act(async () => {
			await api.admin.updateUser(id, changes);
			saved = true;
		});
	}

	const reset = () =>
		confirm('Wachtwoord, authenticator-app, passkeys en herstelcodes van dit account vervallen en alle sessies worden beëindigd. Doorgaan?') &&
		act(async () => {
			link = await api.admin.resetUser(id);
		});

	const toggleDisabled = () => {
		const disable = user?.status !== 'disabled';
		if (disable && !confirm('Account uitschakelen? De gebruiker wordt direct overal uitgelogd.')) return;
		return act(() => api.admin.setDisabled(id, disable));
	};

	const toggleGroup = (groupId: string, member: boolean) => act(() => api.admin.setMember(groupId, id, member));
</script>

<svelte:head>
	<title>{user?.display_name ?? 'Gebruiker'} · Beheer · Cloud</title>
</svelte:head>

<p><a href="/admin/users">← Gebruikers</a></p>

<ErrorNotice {error} />
{#if link && user}<ActivationLink {link} username={user.username} />{/if}

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
					<input bind:value={displayName} required maxlength="100" />
				</label>
				<label class="check">
					<input type="checkbox" bind:checked={isAdmin} disabled={self} /> Beheerder
				</label>
				<div><button class="button button--primary" disabled={busy}>Opslaan</button></div>
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
						<button class="button" onclick={reset} disabled={busy || user.status === 'disabled'}>Account resetten</button>
						<button class="button" onclick={toggleDisabled} disabled={busy}>
							{user.status === 'disabled' ? 'Inschakelen' : 'Uitschakelen'}
						</button>
					</div>
					<p class="muted small">Resetten geeft een nieuwe activatielink, bijvoorbeeld als iemand zijn telefoon kwijt is.</p>
				{/if}
			</section>

			<section class="panel">
				<h2>Groepen</h2>
				{#if groups.length === 0}
					<p class="muted">Er zijn nog geen groepen. <a href="/admin/groups">Groep aanmaken</a></p>
				{:else}
					{#each groups as g (g.id)}
						<label class="check">
							<input type="checkbox" checked={memberOf.has(g.id)} disabled={busy} onchange={(e) => toggleGroup(g.id, e.currentTarget.checked)} />
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
