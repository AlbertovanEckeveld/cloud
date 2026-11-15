<script lang="ts">
	// An app signed the user out (RP-initiated logout). The user decides
	// whether to sign out of Cloud and all other apps as well.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, errorMessage, type LogoutRequest } from '#lib/api.ts';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const id = page.url.searchParams.get('request');
	let request = $state<LogoutRequest | null>(null);
	let error = $state<string | null>(null);
	let busy = $state(false);

	onMount(async () => {
		if (!id) return;
		try {
			request = await api.logoutRequest(id);
		} catch (e) {
			error = errorMessage(e);
		}
	});

	const back = () => request && location.assign(request.redirect);

	async function everywhere() {
		busy = true;
		try {
			await api.logoutEverywhere();
			if (request) location.assign(request.redirect);
			else location.assign('/login');
		} catch (e) {
			error = errorMessage(e);
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Uitgelogd · Cloud</title>
</svelte:head>

<div class="panel auth-card">
	<h1>Uitgelogd</h1>
	{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
	<p class="lead">
		{request ? `Je bent uitgelogd bij ${request.app_name}.` : 'Je bent uitgelogd bij de app.'}
	</p>
	{#if data.user}
		<p>Je bent nog wel ingelogd bij Cloud als {data.user.display_name}.</p>
		<div class="actions">
			<button class="button button--primary" onclick={everywhere} disabled={busy}>Ook overal uitloggen</button>
			{#if request}<button class="button" onclick={back}>Ingelogd blijven</button>{/if}
		</div>
	{:else if request}
		<button class="button" onclick={back}>Terug naar {request.app_name}</button>
	{:else}
		<a class="button" href="/login">Inloggen</a>
	{/if}
</div>

<style>
	.actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
</style>
