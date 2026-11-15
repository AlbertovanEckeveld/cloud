<script lang="ts">
	// Shows a freshly issued activation link once, for the admin to pass on.
	import type { Activation } from '#lib/api.ts';

	let { activation, username, email }: { activation: Activation; username: string; email: string } = $props();
	let copied = $state(false);

	async function copy() {
		await navigator.clipboard.writeText(activation.link);
		copied = true;
	}
</script>

<div class="flash flash--success" role="status">
	<p>
		{#if activation.emailed}
			De activatielink is naar <strong>{email}</strong> gemaild. Komt de mail niet aan, geef deze link dan
			via een privékanaal door. Hij is 72 uur geldig en eenmalig te gebruiken.
		{:else}
			Activatielink voor <strong>{username}</strong>, 72 uur geldig en eenmalig te gebruiken. Geef hem
			alleen via een privékanaal door: wie de link heeft, kan het account overnemen.
		{/if}
	</p>
	<div class="row">
		<code class="link">{activation.link}</code>
		<button class="button" onclick={copy}>{copied ? 'Gekopieerd' : 'Kopiëren'}</button>
	</div>
</div>

<style>
	p {
		margin: 0 0 0.5rem;
	}
	.row {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.link {
		flex: 1;
		overflow-wrap: anywhere;
		user-select: all;
	}
</style>
