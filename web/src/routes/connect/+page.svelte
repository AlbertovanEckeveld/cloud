<script lang="ts">
	// An app sent the user here to sign in (OpenID Connect). With a session
	// we continue at once (single sign-on); otherwise the user signs in first.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError, errorMessage, type OidcRequest } from '#lib/api.ts';
	import LoginForm from '#lib/components/LoginForm.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const id = page.url.searchParams.get('request') ?? '';

	let request = $state<OidcRequest | null>(null);
	let view = $state<'loading' | 'login' | 'denied' | 'invalid'>('loading');
	let error = $state<string | null>(null);

	const cancel = () => location.assign(api.oidcDenyURL(id));

	async function approve() {
		try {
			location.assign(await api.oidcApprove(id));
		} catch (e) {
			if (e instanceof ApiError && e.code === 'app_access_denied') {
				view = 'denied';
			} else if (e instanceof ApiError && (e.code === 'reauth_required' || e.status === 401)) {
				// The app wants a fresh sign-in (prompt=login or max_age).
				view = 'login';
			} else {
				error = errorMessage(e);
				view = 'invalid';
			}
		}
	}

	onMount(async () => {
		try {
			request = await api.oidcRequest(id);
		} catch {
			view = 'invalid';
			return;
		}
		if (data.user) {
			await approve();
		} else if (request.prompt_none) {
			location.assign(api.oidcDenyURL(id, 'login_required'));
		} else {
			view = 'login';
		}
	});
</script>

<svelte:head>
	<title>Inloggen bij {request?.app_name ?? 'app'} · Cloud</title>
</svelte:head>

<div class="panel auth-card">
	{#if view === 'invalid'}
		<h1>Inloggen bij app</h1>
		<p class="flash flash--error" role="alert">
			{error ?? 'Dit inlogverzoek is ongeldig of verlopen. Ga terug naar de app en probeer het opnieuw.'}
		</p>
	{:else if request}
		<h1>Inloggen bij {request.app_name}</h1>
		{#if view === 'login'}
			<p class="lead">Log in met je Cloud-account om verder te gaan naar {request.app_name}.</p>
			<LoginForm onDone={approve} />
			<p class="cancel"><button class="link" onclick={cancel}>Annuleren en terug naar de app</button></p>
		{:else if view === 'denied'}
			<p class="flash flash--error" role="alert">
				Je hebt geen toegang tot {request.app_name}. Vraag een beheerder om je toe te voegen aan de juiste groep.
			</p>
			<button class="button" onclick={cancel}>Terug naar de app</button>
		{:else}
			<p class="lead">Even geduld, je wordt doorgestuurd…</p>
		{/if}
	{:else}
		<p class="muted">Laden…</p>
	{/if}
</div>

<style>
	.cancel {
		margin: 1.5rem 0 0;
		text-align: center;
	}
	.link {
		font: inherit;
		font-size: 0.85rem;
		background: none;
		border: 0;
		padding: 0;
		color: var(--muted);
		cursor: pointer;
	}
	.link:hover {
		color: var(--text-strong);
		text-decoration: underline;
	}
</style>
