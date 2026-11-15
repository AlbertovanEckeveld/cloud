<script lang="ts">
	// Shows a freshly issued activation link once, for the admin to pass on.
	let { link, username }: { link: string; username: string } = $props();
	let copied = $state(false);

	async function copy() {
		await navigator.clipboard.writeText(link);
		copied = true;
	}
</script>

<div class="flash flash--success" role="status">
	<p>
		Activatielink voor <strong>{username}</strong>, 72 uur geldig en eenmalig te gebruiken. Geef hem
		alleen via een privékanaal door: wie de link heeft, kan het account overnemen.
	</p>
	<div class="row">
		<code class="link">{link}</code>
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
