<script lang="ts">
	// Fields of an OpenID Connect app. Used for creating and for editing.
	import { untrack } from 'svelte';
	import type { Group, OidcClient, OidcClientInput } from '#lib/api.ts';

	let {
		app = null,
		groups,
		busy,
		submitLabel,
		onSubmit
	}: {
		app?: OidcClient | null;
		groups: Group[];
		busy: boolean;
		submitLabel: string;
		onSubmit: (input: OidcClientInput) => void;
	} = $props();

	// The form edits a copy; the initial values come from the app, if any.
	// The parent re-creates the form ({#key}) when the app changes.
	const initial = untrack(() => app);
	let name = $state(initial?.name ?? '');
	let confidential = $state(initial?.confidential ?? true);
	let redirects = $state(initial?.redirect_uris.join('\n') ?? '');
	let logoutRedirects = $state(initial?.post_logout_redirect_uris.join('\n') ?? '');
	let groupId = $state(initial?.allowed_group?.id ?? '');

	const lines = (s: string) => s.split('\n').map((l) => l.trim()).filter(Boolean);

	function submit(e: SubmitEvent) {
		e.preventDefault();
		onSubmit({
			name,
			confidential,
			redirect_uris: lines(redirects),
			post_logout_redirect_uris: lines(logoutRedirects),
			allowed_group_id: groupId || null
		});
	}
</script>

<form class="form" onsubmit={submit}>
	<label class="field">
		<span>Naam</span>
		<input bind:value={name} required maxlength="64" placeholder="bijv. Grafana" />
	</label>
	{#if !app}
		<fieldset class="field">
			<span>Soort app</span>
			<label class="check"><input type="radio" bind:group={confidential} value={true} /> Server-app met client-secret (bijv. Grafana, Home Assistant)</label>
			<label class="check"><input type="radio" bind:group={confidential} value={false} /> Publieke app zonder secret, met PKCE (mobiel, desktop, browser)</label>
		</fieldset>
	{/if}
	<label class="field">
		<span>Redirect-URI's (één per regel)</span>
		<textarea bind:value={redirects} rows="3" required spellcheck="false" placeholder="https://grafana.example.com/login/generic_oauth"></textarea>
		<p class="field__hint">Moet exact overeenkomen met wat de app meestuurt. Alleen https, of http naar localhost.</p>
	</label>
	<label class="field">
		<span>Redirect-URI's na uitloggen (optioneel)</span>
		<textarea bind:value={logoutRedirects} rows="2" spellcheck="false"></textarea>
	</label>
	<label class="field">
		<span>Toegang</span>
		<select bind:value={groupId}>
			<option value="">Alle gebruikers</option>
			{#each groups as g (g.id)}
				<option value={g.id}>Alleen groep {g.name}</option>
			{/each}
		</select>
	</label>
	<div><button class="button button--primary" disabled={busy}>{busy ? 'Bezig…' : submitLabel}</button></div>
</form>

<style>
	fieldset {
		border: 0;
		padding: 0;
		margin: 0;
	}
	textarea {
		font-family: var(--mono);
		font-size: 0.85rem;
		color: var(--text-strong);
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 0.55rem 0.7rem;
		resize: vertical;
	}
</style>
