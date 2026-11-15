<script lang="ts">
	import { onMount } from 'svelte';
	import { goto, replaceState } from '$app/navigation';
	import { api, errorMessage, type TotpEnrollment } from '#lib/api.ts';
	import { groupSecret } from '#lib/format.ts';
	import CodeField from '#lib/components/CodeField.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const minLength = 12;

	let step = $state<'password' | 'authenticator' | 'invalid'>('password');
	let token = '';
	let password = $state('');
	let confirmation = $state('');
	let code = $state('');
	let enrollment = $state<TotpEnrollment | null>(null);
	let error = $state<string | null>(null);
	let busy = $state(false);

	onMount(() => {
		if (data.session?.state === 'mfa_enrollment_required') {
			// Reloaded after setting the password: continue with the app.
			startEnrollment();
			return;
		}
		token = location.hash.slice(1);
		// Remove the token from the address bar and history right away.
		replaceState(location.pathname, {});
		if (!token) step = 'invalid';
	});

	async function startEnrollment() {
		step = 'authenticator';
		try {
			enrollment = await api.beginTotpEnrollment();
		} catch (err) {
			error = errorMessage(err);
		}
	}

	async function submitPassword(e: SubmitEvent) {
		e.preventDefault();
		error = null;
		if (password !== confirmation) {
			error = 'De wachtwoorden zijn niet gelijk.';
			return;
		}
		busy = true;
		try {
			await api.activate(token, password);
			await startEnrollment();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function submitCode(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			await api.confirmTotpEnrollment(code);
			await goto('/', { invalidateAll: true });
		} catch (err) {
			error = errorMessage(err);
			code = '';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Account activeren · Cloud</title>
</svelte:head>

<div class="panel auth-card">
	<h1>Account activeren</h1>

	{#if step === 'invalid'}
		<p class="flash flash--error" role="alert">
			Deze activatielink is onvolledig. Open de link precies zoals je hem hebt gekregen, of vraag een
			nieuwe aan.
		</p>
	{:else if step === 'password'}
		<p class="lead">Stap 1 van 2: kies een wachtwoord.</p>
		{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
		<form class="form" onsubmit={submitPassword}>
			<label class="field">
				<span>Nieuw wachtwoord</span>
				<input type="password" bind:value={password} autocomplete="new-password" minlength={minLength} required />
				<p class="field__hint">Minstens {minLength} tekens. Een zin van een paar woorden werkt goed.</p>
			</label>
			<label class="field">
				<span>Herhaal wachtwoord</span>
				<input type="password" bind:value={confirmation} autocomplete="new-password" required />
			</label>
			<button class="button button--primary" disabled={busy}>{busy ? 'Bezig…' : 'Volgende'}</button>
		</form>
	{:else}
		<p class="lead">Stap 2 van 2: koppel een authenticator-app, bijvoorbeeld Aegis, 2FAS of Google Authenticator.</p>
		{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
		{#if enrollment}
			<ol class="steps">
				<li>
					Scan deze QR-code met de app.
					<img class="qr" src={enrollment.qr} alt="QR-code om je account aan de authenticator-app toe te voegen" />
					<details>
						<summary>Kun je niet scannen?</summary>
						<p>
							Op je telefoon: <a href={enrollment.uri}>open in authenticator-app</a>, of voeg handmatig een
							account toe met deze sleutel:
						</p>
						<code class="secret">{groupSecret(enrollment.secret)}</code>
					</details>
				</li>
				<li>Vul de code in die de app toont.</li>
			</ol>
			<form class="form" onsubmit={submitCode}>
				<CodeField bind:value={code} />
				<button class="button button--primary" disabled={busy || code.length !== 6}>
					{busy ? 'Bezig…' : 'Activeren'}
				</button>
			</form>
		{/if}
	{/if}
</div>

<style>
	.steps {
		padding-left: 1.2rem;
		margin: 0 0 1.25rem;
	}
	.steps li + li {
		margin-top: 0.5rem;
	}
	.qr {
		display: block;
		width: 200px;
		height: 200px;
		margin: 0.75rem 0;
		border-radius: 8px;
		image-rendering: pixelated;
	}
	details p {
		margin: 0.5rem 0 0;
	}
	summary {
		cursor: pointer;
		color: var(--muted);
	}
	.secret {
		display: block;
		margin-top: 0.5rem;
		padding: 0.5rem 0.75rem;
		font-size: 1rem;
		letter-spacing: 0.05em;
		word-break: break-all;
		user-select: all;
	}
</style>
