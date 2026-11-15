<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Passkey } from '#lib/api.ts';
	import { dateTime, timeAgo } from '#lib/format.ts';
	import { createPasskey, passkeyUnavailableReason } from '#lib/webauthn.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	const unavailable = passkeyUnavailableReason();
	let passkeys = $state<Passkey[] | null>(null);
	let name = $state('');
	let error = $state<unknown>(null);
	let busy = $state(false);

	async function load() {
		try {
			passkeys = await api.passkeys();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function add(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			const ceremony = await api.passkeyRegistrationBegin();
			const credential = await createPasskey(ceremony.options);
			await api.passkeyRegistrationFinish(ceremony.challenge_id, name, credential);
			name = '';
			await load();
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}

	async function remove(p: Passkey) {
		if (!confirm(`Passkey "${p.name}" verwijderen?`)) return;
		error = null;
		try {
			await api.deletePasskey(p.id);
			await load();
		} catch (err) {
			error = err;
		}
	}
</script>

<section class="panel" aria-labelledby="passkeys-title">
	<h2 id="passkeys-title">Passkeys</h2>
	<p class="muted small">
		Inloggen met vingerafdruk, gezicht of pincode van je apparaat. Veiliger dan een wachtwoord:
		een passkey werkt alleen op deze site en kan niet worden afgeluisterd.
	</p>
	<ErrorNotice {error} />

	{#if passkeys && passkeys.length > 0}
		<ul class="list">
			{#each passkeys as p (p.id)}
				<li>
					<div>
						<span class="list__title">{p.name}</span>
						<span class="badge">{p.synced ? 'Gesynchroniseerd' : 'Op één apparaat'}</span>
						<span class="muted small block">
							Toegevoegd <time datetime={p.created_at} title={dateTime(p.created_at)}>{timeAgo(p.created_at)}</time>
							· {#if p.last_used_at}laatst gebruikt <time datetime={p.last_used_at} title={dateTime(p.last_used_at)}>{timeAgo(p.last_used_at)}</time>{:else}nog niet gebruikt{/if}
						</span>
					</div>
					<button class="button" onclick={() => remove(p)}>Verwijderen</button>
				</li>
			{/each}
		</ul>
	{:else if passkeys}
		<p class="muted">Je hebt nog geen passkeys.</p>
	{/if}

	{#if !unavailable}
		<form class="add" onsubmit={add}>
			<input bind:value={name} placeholder="Naam, bijv. Telefoon" maxlength="64" aria-label="Naam van de passkey" />
			<button class="button button--primary" disabled={busy}>{busy ? 'Bezig…' : 'Passkey toevoegen'}</button>
		</form>
	{:else}
		<p class="muted small">{unavailable}</p>
	{/if}
</section>
