<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AccountStatus, type AdminUser } from '#lib/api.ts';
	import { dateTime, timeAgo } from '#lib/format.ts';
	import ActivationLink from '#lib/components/ActivationLink.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	let users = $state<AdminUser[] | null>(null);
	let error = $state<unknown>(null);

	let inviting = $state(false);
	let username = $state('');
	let displayName = $state('');
	let isAdmin = $state(false);
	let busy = $state(false);
	let issued = $state<{ username: string; link: string } | null>(null);

	const statusLabels: Record<AccountStatus, string> = {
		active: 'Actief',
		pending: 'Wacht op activatie',
		disabled: 'Uitgeschakeld'
	};

	async function load() {
		try {
			users = await api.admin.users();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function invite(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			const link = await api.admin.invite(username, displayName, isAdmin);
			issued = { username: username.trim().toLowerCase(), link };
			username = displayName = '';
			isAdmin = inviting = false;
			await load();
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Gebruikers · Beheer · Cloud</title>
</svelte:head>

<div class="page-header">
	<h1>Gebruikers</h1>
	<button class="button button--primary" onclick={() => ((inviting = !inviting), (issued = null))}>
		{inviting ? 'Annuleren' : 'Gebruiker uitnodigen'}
	</button>
</div>

<ErrorNotice {error} />
{#if issued}<ActivationLink link={issued.link} username={issued.username} />{/if}

{#if inviting}
	<form class="panel form invite" onsubmit={invite}>
		<label class="field">
			<span>Gebruikersnaam</span>
			<input bind:value={username} autocomplete="off" autocapitalize="none" required maxlength="32" />
			<p class="field__hint">Kan later niet worden gewijzigd. Kleine letters, cijfers, punt, streepje of underscore.</p>
		</label>
		<label class="field">
			<span>Naam</span>
			<input bind:value={displayName} autocomplete="off" required maxlength="100" />
		</label>
		<label class="check">
			<input type="checkbox" bind:checked={isAdmin} /> Beheerder
		</label>
		<div><button class="button button--primary" disabled={busy}>{busy ? 'Bezig…' : 'Uitnodigen'}</button></div>
	</form>
{/if}

{#if users}
	<div class="panel table-wrap">
		<table class="table">
			<thead>
				<tr><th>Naam</th><th>Gebruikersnaam</th><th>Rol</th><th>Status</th><th>Laatst ingelogd</th></tr>
			</thead>
			<tbody>
				{#each users as u (u.id)}
					<tr>
						<td><a href="/admin/users/{u.id}">{u.display_name}</a></td>
						<td><code>{u.username}</code></td>
						<td>{u.is_admin ? 'Beheerder' : 'Gebruiker'}</td>
						<td>
							<span class="badge" class:badge--ok={u.status === 'active'} class:badge--error={u.status === 'disabled'}>
								{statusLabels[u.status]}
							</span>
						</td>
						<td>
							{#if u.last_login_at}
								<time datetime={u.last_login_at} title={dateTime(u.last_login_at)}>{timeAgo(u.last_login_at)}</time>
							{:else}<span class="muted">Nooit</span>{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		flex-wrap: wrap;
		margin-bottom: 1.5rem;
	}
	.invite {
		max-width: 480px;
		margin-bottom: 1.5rem;
	}
</style>
