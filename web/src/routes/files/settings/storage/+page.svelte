<script lang="ts">
	// Storage per user: what each account uses and its limit (drive.quota.manage).
	// The list of accounts is identity's, so it needs identity.users.view too.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, type AdminUser, type Usage } from '#lib/api.ts';
	import { formatBytes } from '#lib/format.ts';
	import { can } from '#lib/permissions.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	type Row = { user: AdminUser; usage: Usage; limitGB: string };

	let rows = $state<Row[] | null>(null);
	let error = $state<unknown>(null);
	let busy = $state('');
	let saved = $state('');
	const mayListUsers = $derived(can(page.data.user, 'identity.users.view'));

	const gb = (bytes: number | null) => (bytes === null ? '' : String(bytes / 1024 ** 3));

	async function load() {
		if (!mayListUsers) return;
		try {
			const users = await api.admin.users();
			const usages = await Promise.all(users.map((u) => api.drive.userUsage(u.id)));
			rows = users
				.map((user, i) => ({ user, usage: usages[i], limitGB: gb(usages[i].limit_bytes) }))
				.sort((a, b) => b.usage.used_bytes - a.usage.used_bytes);
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function save(e: SubmitEvent, row: Row) {
		e.preventDefault();
		saved = '';
		error = null;
		const value = row.limitGB.trim() === '' ? null : Number(row.limitGB.replace(',', '.'));
		if (value !== null && (!Number.isFinite(value) || value < 0)) {
			error = new Error('Vul een aantal GB in, of laat het veld leeg voor geen limiet.');
			return;
		}
		busy = row.user.id;
		try {
			row.usage = await api.drive.setQuota(row.user.id, value === null ? null : Math.round(value * 1024 ** 3));
			row.limitGB = gb(row.usage.limit_bytes);
			saved = row.user.id;
		} catch (err) {
			error = err;
		} finally {
			busy = '';
		}
	}
</script>

<svelte:head>
	<title>Opslag per gebruiker · Drive-instellingen · Cloud</title>
</svelte:head>

<h1>Opslag per gebruiker</h1>
<p class="muted">Gebruik telt alles mee wat iemand bezit, ook de prullenbak en oude versies. Laat de limiet leeg voor geen limiet.</p>

<ErrorNotice {error} />

{#if !mayListUsers}
	<p class="muted">Om de lijst met gebruikers te zien heb je ook het recht "Gebruikers en hun activiteit zien" nodig.</p>
{:else if rows}
	<div class="panel table-wrap">
		<table class="table">
			<thead><tr><th>Gebruiker</th><th class="num">Gebruikt</th><th class="col-optional">Oude versies</th><th>Limiet</th></tr></thead>
			<tbody>
				{#each rows as row (row.user.id)}
					<tr>
						<td>{row.user.display_name}<span class="muted small block"><code>{row.user.username}</code></span></td>
						<td class="num">{formatBytes(row.usage.used_bytes)}</td>
						<td class="muted col-optional">{formatBytes(row.usage.old_versions_bytes)}</td>
						<td>
							<form class="add" onsubmit={(e) => save(e, row)}>
								<input bind:value={row.limitGB} inputmode="decimal" placeholder="Geen" aria-label="Limiet van {row.user.display_name} in GB" />
								<span class="muted">GB</span>
								<button class="button" disabled={busy === row.user.id}>Opslaan</button>
								{#if saved === row.user.id}<span class="muted small" role="status">Opgeslagen</span>{/if}
							</form>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{:else if !error}
	<p class="muted">Laden…</p>
{/if}

<style>
	.num {
		text-align: right;
		white-space: nowrap;
	}
	.add input {
		width: 6rem;
	}
</style>
