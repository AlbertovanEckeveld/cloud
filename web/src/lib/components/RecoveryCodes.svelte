<script lang="ts">
	// Shows freshly generated recovery codes once, with ways to keep them.
	let { codes }: { codes: string[] } = $props();

	let copied = $state(false);
	const text = $derived(
		'Herstelcodes voor Cloud. Elke code werkt één keer, in plaats van de code uit je authenticator-app.\n\n' +
			codes.join('\n') +
			'\n'
	);
	const href = $derived(URL.createObjectURL(new Blob([text], { type: 'text/plain' })));

	$effect(() => {
		const url = href;
		return () => URL.revokeObjectURL(url);
	});

	async function copy() {
		await navigator.clipboard.writeText(text);
		copied = true;
	}
</script>

<ul class="codes">
	{#each codes as code (code)}
		<li><code>{code}</code></li>
	{/each}
</ul>
<div class="actions">
	<a class="button" {href} download="cloud-herstelcodes.txt">Downloaden</a>
	<button type="button" class="button" onclick={copy}>{copied ? 'Gekopieerd' : 'Kopiëren'}</button>
</div>

<style>
	.codes {
		list-style: none;
		padding: 0;
		margin: 0 0 1rem;
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 0.4rem;
	}
	.codes code {
		display: block;
		padding: 0.35rem 0.25rem;
		text-align: center;
		font-size: 0.82rem;
		white-space: nowrap;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
	}
	.actions a:hover {
		text-decoration: none;
	}
</style>
