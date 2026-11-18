<script lang="ts">
	// AI settings (ADR 0023): providers with an OpenAI-compatible API, and
	// which model answers questions and which makes embeddings. Configured
	// here, not in files; every change is audited and needs a recent sign-in.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Icon from '#lib/components/Icon.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import { api, type AIIndexStatus, type AIProvider, type AISettings } from '#lib/api.ts';
	import { isReauthError, reauthenticate } from '#lib/auth.ts';
	import { timeAgo } from '#lib/format.ts';

	type Draft = { id: string | null; name: string; base_url: string; api_key: string; changeKey: boolean; consent: boolean };

	let providers = $state<AIProvider[]>([]);
	let settings = $state<AISettings | null>(null);
	let index = $state<AIIndexStatus | null>(null);
	let models = $state<Record<string, string[]>>({});
	let error = $state<unknown>(null);
	let notice = $state('');
	let busy = $state(false);
	let draft = $state<Draft | null>(null);

	const nameOf = (id: string | null) => providers.find((p) => p.id === id)?.name ?? '';
	// Embedding models usually say so in their name: offer those for
	// embeddings (first) and the others for the chat.
	const looksEmbedding = (m: string) => /embed/i.test(m);
	const chatModels = (id: string) => (models[id] ?? []).filter((m) => !looksEmbedding(m));
	const embedModels = (id: string) => [...(models[id] ?? [])].sort((a, b) => Number(looksEmbedding(b)) - Number(looksEmbedding(a)));
	const external = (id: string | null) => providers.find((p) => p.id === id)?.external ?? false;

	async function load() {
		try {
			[providers, settings, index] = await Promise.all([api.assistant.providers(), api.assistant.settings(), api.assistant.index()]);
		} catch (e) {
			error = e;
		}
	}
	onMount(() => {
		load();
		// The index fills in the background: show its progress.
		const timer = setInterval(async () => {
			try {
				index = await api.assistant.index();
			} catch {
				/* the next tick tries again */
			}
		}, 5000);
		return () => clearInterval(timer);
	});

	async function act(action: () => Promise<unknown>, done = '') {
		busy = true;
		error = null;
		notice = '';
		try {
			await action();
			await load();
			notice = done;
		} catch (e) {
			if (isReauthError(e)) return reauthenticate(page.url.pathname);
			error = e;
		} finally {
			busy = false;
		}
	}

	const add = () => (draft = { id: null, name: '', base_url: 'http://localhost:11434/v1', api_key: '', changeKey: true, consent: false });
	const edit = (p: AIProvider) => (draft = { id: p.id, name: p.name, base_url: p.base_url, api_key: '', changeKey: false, consent: p.external });

	function saveProvider(e: SubmitEvent) {
		e.preventDefault();
		const d = draft;
		if (!d) return;
		const body: { name: string; base_url: string; api_key?: string; consent_external: boolean } = {
			name: d.name,
			base_url: d.base_url,
			consent_external: d.consent
		};
		if (d.changeKey) body.api_key = d.api_key;
		return act(async () => {
			const p = await api.assistant.saveProvider(d.id, body);
			draft = null;
			await test(p.id);
		}, 'Aanbieder opgeslagen.');
	}

	async function test(id: string) {
		try {
			models[id] = await api.assistant.testProvider(id);
			notice = `Verbinding met ${nameOf(id)} werkt: ${models[id].length} modellen.`;
		} catch (e) {
			error = e;
		}
	}

	const remove = (p: AIProvider) =>
		confirm(`Aanbieder "${p.name}" verwijderen? Instellingen die hem gebruiken vervallen.`) &&
		act(() => api.assistant.deleteProvider(p.id), 'Aanbieder verwijderd.');

	function saveSettings(e: SubmitEvent) {
		e.preventDefault();
		const s = settings;
		if (!s) return;
		if (s.embed_provider && external(s.embed_provider) &&
			!confirm(`Het embedding-model draait bij een externe aanbieder (${nameOf(s.embed_provider)}). Dan gaat de tekst van álle bestanden daarheen om ze doorzoekbaar te maken. Doorgaan?`)) {
			return;
		}
		return act(() => api.assistant.saveSettings(s), 'Instellingen opgeslagen. Het indexeren begint direct.');
	}

	// When a provider changes, its model list is fetched for the picker.
	$effect(() => {
		for (const id of [settings?.chat_provider, settings?.embed_provider]) {
			if (id && !models[id]) test(id);
		}
	});
</script>

<svelte:head>
	<title>AI-instellingen · Cloud</title>
</svelte:head>

<p><a href="/assistant">← Assistent</a></p>
<h1>AI-instellingen</h1>
<p class="muted lead">
	De assistent werkt met elke aanbieder met een OpenAI-compatibele API: lokaal Ollama of llama.cpp, of een externe dienst.
	Lokaal blijven je bestanden thuis. Bij een externe aanbieder gaan vragen en stukken tekst naar die dienst.
</p>

<ErrorNotice {error} />
{#if notice}<p class="flash flash--success" role="status">{notice}</p>{/if}

<section class="panel">
	<div class="head">
		<h2>Aanbieders</h2>
		<button class="button" onclick={add}><span class="with-icon"><Icon name="plus" />Aanbieder toevoegen</span></button>
	</div>
	{#if providers.length === 0}
		<p class="muted">Nog geen aanbieders. Voor een lokaal model: installeer Ollama en vul http://localhost:11434/v1 in.</p>
	{:else}
		<ul class="list">
			{#each providers as p (p.id)}
				<li>
					<div>
						<span class="list__title">{p.name}</span>
						{#if p.external}<span class="badge badge--error">Extern</span>{:else}<span class="badge badge--ok">Lokaal</span>{/if}
						<span class="muted small block"><code>{p.base_url}</code> · {p.has_key ? 'met API-sleutel' : 'zonder sleutel'} · gewijzigd {timeAgo(p.updated_at)}</span>
						{#if models[p.id]}<span class="muted small block">{models[p.id].length} modellen beschikbaar</span>{/if}
					</div>
					<div class="row">
						<button class="button" disabled={busy} onclick={() => test(p.id)}>Verbinding testen</button>
						<button class="button" disabled={busy} onclick={() => edit(p)}>Wijzigen</button>
						<button class="icon-button" aria-label="Verwijderen" title="Verwijderen" disabled={busy} onclick={() => remove(p)}><Icon name="trash" /></button>
					</div>
				</li>
			{/each}
		</ul>
	{/if}

	{#if draft}
		<form class="form editor" onsubmit={saveProvider}>
			<h3>{draft.id ? 'Aanbieder wijzigen' : 'Nieuwe aanbieder'}</h3>
			<label class="field"><span>Naam</span><input bind:value={draft.name} required maxlength="60" placeholder="Ollama thuis" /></label>
			<label class="field">
				<span>Adres van de API</span>
				<input bind:value={draft.base_url} required maxlength="500" inputmode="url" />
				<p class="field__hint">Bijvoorbeeld http://localhost:11434/v1 (Ollama) of https://generativelanguage.googleapis.com/v1beta/openai (Gemini).</p>
			</label>
			{#if draft.id && !draft.changeKey}
				<button type="button" class="link-button" onclick={() => draft && (draft.changeKey = true)}>API-sleutel vervangen of weghalen</button>
			{:else}
				<label class="field">
					<span>API-sleutel (optioneel)</span>
					<input type="password" bind:value={draft.api_key} maxlength="500" autocomplete="off" />
					<p class="field__hint">Wordt versleuteld bewaard en is daarna niet meer te zien. {draft.id ? 'Leeg laten haalt de sleutel weg.' : ''}</p>
				</label>
			{/if}
			<label class="check">
				<input type="checkbox" bind:checked={draft.consent} />
				<span>Als dit een externe aanbieder is: ik begrijp dat vragen en tekst uit bestanden naar deze dienst gaan.</span>
			</label>
			<div class="row">
				<button class="button button--primary" disabled={busy}>Opslaan en testen</button>
				<button type="button" class="button" onclick={() => (draft = null)}>Annuleren</button>
			</div>
		</form>
	{/if}
</section>

{#if settings && providers.length > 0}
	<form class="panel form" onsubmit={saveSettings}>
		<h2>Modellen</h2>
		<fieldset>
			<legend>Chat: beantwoordt vragen</legend>
			<select bind:value={settings.chat_provider} aria-label="Aanbieder voor de chat">
				<option value={null}>Uit</option>
				{#each providers as p (p.id)}<option value={p.id}>{p.name}{p.external ? ' (extern)' : ''}</option>{/each}
			</select>
			{#if settings.chat_provider}
				<input bind:value={settings.chat_model} list="models-chat" placeholder="Model, bijv. qwen2.5:3b" aria-label="Chatmodel" required />
				<datalist id="models-chat">{#each chatModels(settings.chat_provider) as m (m)}<option value={m}></option>{/each}</datalist>
			{/if}
		</fieldset>
		<fieldset>
			<legend>Embeddings: maakt zoeken op betekenis mogelijk</legend>
			<select bind:value={settings.embed_provider} aria-label="Aanbieder voor embeddings">
				<option value={null}>Uit</option>
				{#each providers as p (p.id)}<option value={p.id}>{p.name}{p.external ? ' (extern)' : ''}</option>{/each}
			</select>
			{#if settings.embed_provider}
				<input bind:value={settings.embed_model} list="models-embed" placeholder="Model, bijv. bge-m3" aria-label="Embedding-model" required />
				<datalist id="models-embed">{#each embedModels(settings.embed_provider) as m (m)}<option value={m}></option>{/each}</datalist>
				<p class="field__hint">
					Kies een embedding-model (meestal met "embed" in de naam); een chatmodel werkt hier niet. Een ander model kiezen
					betekent alle bestanden opnieuw indexeren.
				</p>
			{/if}
		</fieldset>
		<div><button class="button button--primary" disabled={busy}>Opslaan</button></div>
	</form>
{/if}

{#if index?.configured}
	<section class="panel">
		<h2>Index</h2>
		<p>{index.indexed} van {index.files} bestanden doorzoekbaar op betekenis{index.failed ? `, ${index.failed} mislukt` : ''}.</p>
		<progress max={index.files || 1} value={index.indexed}></progress>
		{#if index.last_error}<p class="muted small">Laatste fout: {index.last_error} Het wordt later opnieuw geprobeerd.</p>{/if}
	</section>
{/if}

<style>
	.lead {
		max-width: 70ch;
	}
	.head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		flex-wrap: wrap;
	}
	.row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		align-items: center;
	}
	.list li {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}
	.editor {
		margin-top: 1rem;
		border-top: 1px solid var(--border);
		padding-top: 1rem;
	}
	fieldset {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.75rem 1rem;
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	legend {
		padding: 0 0.4rem;
		color: var(--muted);
	}
	fieldset input {
		flex: 1 1 14rem;
	}
	progress {
		width: 100%;
	}
	.panel + .panel,
	.panel + form,
	form + .panel {
		margin-top: 1rem;
	}
</style>
