<script lang="ts">
	// Collabora Online in an iframe. The access token goes to the editor in a
	// form post (never in a URL), as the WOPI spec prescribes; the editor then
	// talks to this platform's WOPI endpoints itself. Messages from the editor
	// are only accepted from Collabora's own origin.
	import { onMount, tick } from 'svelte';
	import type { OfficeSession } from '#lib/api.ts';

	let { session, origin, onClose }: { session: OfficeSession; origin: string; onClose: () => void } = $props();

	let form = $state<HTMLFormElement>();

	function message(e: MessageEvent) {
		if (e.origin !== origin || typeof e.data !== 'string') return;
		try {
			const msg = JSON.parse(e.data) as { MessageId?: string };
			if (msg.MessageId === 'UI_Close') onClose();
		} catch {
			// Not a Collabora message.
		}
	}

	onMount(() => {
		tick().then(() => form?.submit());
		window.addEventListener('message', message);
		return () => window.removeEventListener('message', message);
	});
</script>

<form bind:this={form} method="POST" action={session.url} target="office" hidden>
	<input name="access_token" value={session.access_token} />
	<input name="access_token_ttl" value={session.access_token_ttl} />
</form>
<iframe name="office" title="Documenteditor" allow="clipboard-read; clipboard-write; fullscreen" allowfullscreen></iframe>

<style>
	iframe {
		flex: 1;
		width: 100%;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: #fff;
	}
</style>
