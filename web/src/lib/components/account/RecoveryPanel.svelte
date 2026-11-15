<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import ErrorNotice from '../ErrorNotice.svelte';
	import RecoveryCodes from '../RecoveryCodes.svelte';

	let remaining = $state<number | null>(null);
	let codes = $state<string[] | null>(null);
	let error = $state<unknown>(null);
	let busy = $state(false);

	onMount(async () => {
		try {
			remaining = await api.recoveryCodesRemaining();
		} catch (e) {
			error = e;
		}
	});

	async function generate() {
		if (remaining && !confirm('Je huidige herstelcodes vervallen. Doorgaan?')) return;
		busy = true;
		error = null;
		try {
			codes = await api.generateRecoveryCodes();
			remaining = codes.length;
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<section class="panel" aria-labelledby="recovery-title">
	<h2 id="recovery-title">Herstelcodes</h2>
	<p class="muted small">Om in te loggen als je je authenticator-app kwijt bent. Elke code werkt één keer.</p>
	<ErrorNotice {error} />

	{#if codes}
		<p class="flash flash--success" role="status">Bewaar deze codes nu: je ziet ze maar één keer.</p>
		<RecoveryCodes {codes} />
	{:else if remaining !== null}
		<p>
			{#if remaining === 0}
				<span class="badge badge--error">Geen herstelcodes</span>
			{:else}
				<span class="badge" class:badge--ok={remaining > 3} class:badge--error={remaining <= 3}>
					Nog {remaining} {remaining === 1 ? 'code' : 'codes'} over
				</span>
			{/if}
		</p>
		<button class="button" onclick={generate} disabled={busy}>
			{remaining === 0 ? 'Herstelcodes maken' : 'Nieuwe herstelcodes maken'}
		</button>
	{/if}
</section>
