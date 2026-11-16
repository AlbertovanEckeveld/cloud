<script lang="ts">
	// Edits a document in Collabora Online. The access token goes to the
	// editor in a form post (never in a URL), as the WOPI spec prescribes;
	// the editor then talks to this platform's WOPI endpoints itself.
	import { onMount, tick } from 'svelte';
	import { page } from '$app/state';
	import { api, type OfficeInfo, type OfficeSession } from '#lib/api.ts';
	import Icon from '#lib/components/Icon.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	let session = $state<OfficeSession | null>(null);
	let office = $state<OfficeInfo | null>(null);
	let error = $state<unknown>(null);
	let form = $state<HTMLFormElement>();

	const id = page.params.id ?? '';

	function back() {
		if (history.length > 1) history.back();
		else location.assign('/files');
	}

	// Collabora reports through postMessage; only its own origin is trusted.
	function message(e: MessageEvent) {
		if (!office || e.origin !== office.origin || typeof e.data !== 'string') return;
		try {
			const msg = JSON.parse(e.data) as { MessageId?: string };
			if (msg.MessageId === 'UI_Close') back();
		} catch {
			// Not a Collabora message.
		}
	}

	onMount(() => {
		(async () => {
			try {
				[office, session] = await Promise.all([api.drive.office(), api.drive.openInOffice(id)]);
				await tick(); // the form exists once the session is rendered
				form?.submit();
			} catch (e) {
				error = e;
			}
		})();
		window.addEventListener('message', message);
		return () => window.removeEventListener('message', message);
	});
</script>

<svelte:head>
	<title>Document · Cloud</title>
</svelte:head>

<div class="editor">
	<div class="editor__bar">
		<button class="icon-button" aria-label="Terug" title="Terug" onclick={back}><Icon name="back" /></button>
		{#if session && !session.edit}<span class="badge">Alleen lezen</span>{/if}
		<a class="icon-button" href={api.drive.downloadURL(id)} download aria-label="Downloaden" title="Downloaden"><Icon name="download" /></a>
	</div>
	<ErrorNotice {error} />
	{#if session}
		<form bind:this={form} method="POST" action={session.url} target="office" hidden>
			<input name="access_token" value={session.access_token} />
			<input name="access_token_ttl" value={session.access_token_ttl} />
		</form>
		<iframe name="office" title="Documenteditor" allow="clipboard-read; clipboard-write; fullscreen" allowfullscreen></iframe>
	{/if}
</div>

<style>
	.editor {
		display: flex;
		flex-direction: column;
		height: calc(100vh - 7rem);
	}
	.editor__bar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}
	.editor__bar a:last-child {
		margin-left: auto;
	}
	iframe {
		flex: 1;
		width: 100%;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: #fff;
	}
</style>
