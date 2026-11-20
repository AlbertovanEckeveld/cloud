<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Group } from '#lib/api.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import { page } from '$app/state';
	import { can } from '#lib/permissions.ts';

	let groups = $state<Group[] | null>(null);
	let error = $state<unknown>(null);
	let name = $state('');
	let description = $state('');
	let busy = $state(false);

	async function load() {
		try {
			groups = await api.admin.groups();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function create(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			await api.admin.createGroup(name, description);
			name = description = '';
			await load();
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Groepen · Identity · Cloud</title>
</svelte:head>

<h1>Groepen</h1>
<p class="muted">
	Groepen bundelen gebruikers. Apps die via deze cloud inloggen, krijgen de groepsnamen mee en kunnen
	daar rechten aan koppelen.
</p>

<ErrorNotice {error} />

<div class="columns">
	<section class="panel">
		{#if groups && groups.length > 0}
			<ul class="list">
				{#each groups as g (g.id)}
					<li>
						<div>
							<a class="list__title" href="/identity/groups/{g.id}">{g.name}</a>
							{#if g.description}<span class="muted small block">{g.description}</span>{/if}
						</div>
						<span class="badge">{g.member_count} {g.member_count === 1 ? 'lid' : 'leden'}</span>
					</li>
				{/each}
			</ul>
		{:else if groups}
			<p class="muted">Er zijn nog geen groepen.</p>
		{/if}
	</section>

{#if can(page.data.user, 'identity.groups.manage')}
	<form class="panel form" onsubmit={create}>
		<h2>Nieuwe groep</h2>
		<label class="field">
			<span>Naam</span>
			<input bind:value={name} required maxlength="32" autocapitalize="none" placeholder="bijv. familie" />
			<p class="field__hint">Kleine letters, cijfers, punt, streepje of underscore. Apps gebruiken deze naam.</p>
		</label>
		<label class="field">
			<span>Omschrijving (optioneel)</span>
			<input bind:value={description} maxlength="200" />
		</label>
		<div><button class="button button--primary" disabled={busy}>Aanmaken</button></div>
	</form>
{/if}
</div>
