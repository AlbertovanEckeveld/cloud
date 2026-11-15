<script lang="ts">
	// Sign-in with a passkey, or password followed by an authenticator code
	// or recovery code. Calls onDone once the session is fully authenticated.
	import { untrack } from 'svelte';
	import { api, ApiError, errorMessage } from '#lib/api.ts';
	import { getPasskey, passkeysSupported, webauthnErrorMessage } from '#lib/webauthn.ts';
	import CodeField from './CodeField.svelte';

	let { resume = false, onDone }: { resume?: boolean; onDone: () => Promise<void> } = $props();

	// Only the initial value matters; afterwards the form drives the steps.
	let step = $state<'start' | 'code'>(untrack(() => resume) ? 'code' : 'start');
	let useRecovery = $state(false);
	let username = $state('');
	let password = $state('');
	let code = $state('');
	let recoveryCode = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

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
			await onDone();
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
				await onDone();
			} finally {
				code = recoveryCode = '';
			}
		});
	}
</script>

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
