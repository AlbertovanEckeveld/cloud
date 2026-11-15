<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError, errorMessage } from '#lib/api.ts';
	import { safeNext } from '#lib/format.ts';
	import CodeField from '#lib/components/CodeField.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// A page reload between the two steps resumes at the code. Only the state
	// at page load matters; after that this page drives the steps itself.
	const resume = untrack(() => data.session?.state === 'mfa_required');
	let step = $state<'password' | 'code'>(resume ? 'code' : 'password');
	let username = $state('');
	let password = $state('');
	let code = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	async function submitPassword(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			await api.login(username, password);
			step = 'code';
		} catch (err) {
			error = errorMessage(err);
		} finally {
			password = '';
			busy = false;
		}
	}

	async function submitCode(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			await api.loginTotp(code);
			await goto(safeNext(page.url.searchParams.get('next'), page.url.origin), { invalidateAll: true });
		} catch (err) {
			error = errorMessage(err);
			code = '';
			// Too many wrong codes ends the attempt: start over with the password.
			if (err instanceof ApiError && err.status === 401 && err.code === 'unauthenticated') step = 'password';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Inloggen · Cloud</title>
</svelte:head>

<div class="panel auth-card">
	<h1>Inloggen</h1>
	{#if step === 'password'}
		<p class="lead">Log in met je gebruikersnaam en wachtwoord.</p>
		{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
		<form class="form" onsubmit={submitPassword}>
			<label class="field">
				<span>Gebruikersnaam</span>
				<input bind:value={username} autocomplete="username" autocapitalize="none" required />
			</label>
			<label class="field">
				<span>Wachtwoord</span>
				<input type="password" bind:value={password} autocomplete="current-password" required />
			</label>
			<button class="button button--primary" disabled={busy}>{busy ? 'Bezig…' : 'Volgende'}</button>
		</form>
	{:else}
		<p class="lead">Vul de code in uit je authenticator-app.</p>
		{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
		<form class="form" onsubmit={submitCode}>
			<CodeField bind:value={code} />
			<button class="button button--primary" disabled={busy || code.length !== 6}>
				{busy ? 'Bezig…' : 'Inloggen'}
			</button>
		</form>
	{/if}
</div>
