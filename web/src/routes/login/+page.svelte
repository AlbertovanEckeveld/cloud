<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError, errorMessage } from '#lib/api.ts';
	import { safeNext } from '#lib/format.ts';
	import { getPasskey, passkeysSupported, webauthnErrorMessage } from '#lib/webauthn.ts';
	import CodeField from '#lib/components/CodeField.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// A page reload between the two steps resumes at the code. Only the state
	// at page load matters; after that this page drives the steps itself.
	const resume = untrack(() => data.session?.state === 'mfa_required');
	let step = $state<'start' | 'code'>(resume ? 'code' : 'start');
	let useRecovery = $state(false);
	let username = $state('');
	let password = $state('');
	let code = $state('');
	let recoveryCode = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	const done = () => goto(safeNext(page.url.searchParams.get('next'), page.url.origin), { invalidateAll: true });

	async function withBusy(action: () => Promise<void>) {
		busy = true;
		error = null;
		try {
			await action();
		} catch (err) {
			error = webauthnErrorMessage(err) ?? errorMessage(err);
			// Too many wrong codes ends the attempt: start over.
			if (err instanceof ApiError && err.code === 'unauthenticated') step = 'start';
		} finally {
			busy = false;
		}
	}

	const loginWithPasskey = () =>
		withBusy(async () => {
			const ceremony = await api.passkeyLoginBegin();
			const credential = await getPasskey(ceremony.options);
			await api.passkeyLoginFinish(ceremony.challenge_id, credential);
			await done();
		});

	function submitPassword(e: SubmitEvent) {
		e.preventDefault();
		return withBusy(async () => {
			try {
				await api.login(username, password);
				step = 'code';
			} finally {
				password = '';
			}
		});
	}

	function submitCode(e: SubmitEvent) {
		e.preventDefault();
		return withBusy(async () => {
			try {
				if (useRecovery) await api.loginRecovery(recoveryCode);
				else await api.loginTotp(code);
				await done();
			} finally {
				code = recoveryCode = '';
			}
		});
	}
</script>

<svelte:head>
	<title>Inloggen · Cloud</title>
</svelte:head>

<div class="panel auth-card">
	<h1>Inloggen</h1>
	{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}

	{#if step === 'start'}
		{#if passkeysSupported()}
			<button class="button button--primary wide" onclick={loginWithPasskey} disabled={busy}>
				Inloggen met passkey
			</button>
			<p class="divider"><span>of met wachtwoord</span></p>
		{/if}
		<form class="form" onsubmit={submitPassword}>
			<label class="field">
				<span>Gebruikersnaam</span>
				<input bind:value={username} autocomplete="username" autocapitalize="none" required />
			</label>
			<label class="field">
				<span>Wachtwoord</span>
				<input type="password" bind:value={password} autocomplete="current-password" required />
			</label>
			<button class="button" disabled={busy}>{busy ? 'Bezig…' : 'Volgende'}</button>
		</form>
	{:else}
		<form class="form" onsubmit={submitCode}>
			{#if useRecovery}
				<label class="field">
					<span>Herstelcode</span>
					<input bind:value={recoveryCode} autocomplete="off" autocapitalize="none" spellcheck="false" placeholder="xxxx-xxxx-xxxx-xxxx" required />
					<p class="field__hint">Elke herstelcode werkt maar één keer.</p>
				</label>
			{:else}
				<p class="lead">Vul de code in uit je authenticator-app.</p>
				<CodeField bind:value={code} />
			{/if}
			<button class="button button--primary" disabled={busy || (!useRecovery && code.length !== 6)}>
				{busy ? 'Bezig…' : 'Inloggen'}
			</button>
		</form>
		<button class="link" onclick={() => ((useRecovery = !useRecovery), (error = null))}>
			{useRecovery ? 'Toch de authenticator-app gebruiken' : 'Telefoon kwijt? Gebruik een herstelcode'}
		</button>
	{/if}
</div>

<style>
	.wide {
		width: 100%;
	}
	.divider {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		color: var(--muted);
		font-size: 0.85rem;
		margin: 1.25rem 0;
	}
	.divider::before,
	.divider::after {
		content: '';
		flex: 1;
		border-top: 1px solid var(--border);
	}
	.link {
		font: inherit;
		font-size: 0.85rem;
		background: none;
		border: 0;
		padding: 0;
		margin-top: 1rem;
		color: var(--muted);
		cursor: pointer;
	}
	.link:hover {
		color: var(--text-strong);
		text-decoration: underline;
	}
</style>
