<script lang="ts">
	// Shows what an administrator copies into the app's OIDC settings.
	let { issuer, clientId, secret }: { issuer: string; clientId: string; secret: string } = $props();
	let copied = $state('');

	async function copy(label: string, value: string) {
		await navigator.clipboard.writeText(value);
		copied = label;
	}
</script>

<dl class="details creds">
	<dt>Issuer</dt>
	<dd><code>{issuer}</code> <button class="button" onclick={() => copy('issuer', issuer)}>{copied === 'issuer' ? '✓' : 'Kopiëren'}</button></dd>
	<dt>Client-ID</dt>
	<dd><code>{clientId}</code> <button class="button" onclick={() => copy('id', clientId)}>{copied === 'id' ? '✓' : 'Kopiëren'}</button></dd>
	{#if secret}
		<dt>Client-secret</dt>
		<dd>
			<code>{secret}</code> <button class="button" onclick={() => copy('secret', secret)}>{copied === 'secret' ? '✓' : 'Kopiëren'}</button>
			<span class="block small">Je ziet het secret maar één keer. Kwijt? Maak een nieuw aan.</span>
		</dd>
	{/if}
</dl>

<style>
	.creds code {
		overflow-wrap: anywhere;
		user-select: all;
	}
	.creds .button {
		padding: 0.15rem 0.6rem;
		font-size: 0.75rem;
		margin-left: 0.25rem;
	}
</style>
