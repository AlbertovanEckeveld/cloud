<script lang="ts">
	// Search results across everything the user can see.
	import Icon from '#lib/components/Icon.svelte';
	import { api, driveDetail, errorMessage, type SearchResult } from '#lib/api.ts';
	import { dateTime, formatBytes, previewKind, snippetParts, timeAgo } from '#lib/format.ts';
	import PreviewDialog from './PreviewDialog.svelte';
	import { editable } from '#lib/drive-state.svelte.ts';
	import { askAssistant, assistant } from '#lib/assistant-state.svelte.ts';

	let { query }: { query: string } = $props();

	let results = $state<SearchResult[] | null>(null);
	let error = $state<string | null>(null);
	let previewing = $state<number | null>(null);
	const previewable = $derived(results?.filter((r) => r.kind === 'file' && previewKind(r.content_type, r.name)) ?? []);

	$effect(() => {
		const q = query.trim();
		results = null;
		error = null;
		// Wait until the user stops typing instead of searching every key.
		const timer = setTimeout(async () => {
			try {
				// Name, words and, when a model is set up, meaning (ADR 0023).
				const found = (await api.assistant.search(q, ['drive'])).map(driveDetail).filter((r) => r !== null);
				if (query.trim() === q) results = found;
			} catch (e) {
				error = errorMessage(e);
			}
		}, 250);
		return () => clearTimeout(timer);
	});
</script>

<div class="head">
	<h1 class="title">Zoekresultaten voor "{query.trim()}"</h1>
	{#if assistant.available}
		<button class="button with-icon" onclick={() => askAssistant(query.trim())}><Icon name="spark" />Vraag de assistent</button>
	{/if}
</div>
{#if error}
	<p class="flash flash--error" role="alert">{error}</p>
{:else if results === null}
	<p class="muted">Zoeken…</p>
{:else if results.length === 0}
	<p class="muted">Niets gevonden. Er wordt gezocht op naam en in de tekst van bestanden, in je eigen bestanden, teammappen en wat met je gedeeld is.</p>
{:else}
	<section class="panel">
		<table class="table">
			<thead><tr><th>Naam</th><th>Locatie</th><th class="num col-optional">Grootte</th><th class="col-optional">Gewijzigd</th></tr></thead>
			<tbody>
				{#each results as r (r.id)}
					<tr>
						<td class="name">
							{#if r.kind === 'folder'}
								<a class="with-icon" href="/files/{r.id}"><Icon name="folder" />{r.name}</a>
							{:else if editable(r.name) && !previewKind(r.content_type, r.name)}
								<a class="with-icon" href="/edit/{r.id}"><Icon name="file" />{r.name}</a>
							{:else if previewKind(r.content_type, r.name)}
								<button class="link-button with-icon" onclick={() => (previewing = previewable.indexOf(r))}><Icon name="file" />{r.name}</button>
							{:else}
								<a class="with-icon" href={api.drive.downloadURL(r.id)} download><Icon name="file" />{r.name}</a>
							{/if}
							{#if r.snippet}
								<p class="snippet small muted">
									{#each snippetParts(r.snippet) as p, i (i)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}
								</p>
							{/if}
						</td>
						<td class="muted">
							{#if r.location_id}
								<a class="with-icon location" href="/files/{r.location_id}"><Icon name="folder" size={16} />{r.location}</a>
							{:else}
								{r.location}
							{/if}
						</td>
						<td class="num muted col-optional">{r.kind === 'file' ? formatBytes(r.size) : ''}</td>
						<td class="muted col-optional when"><time datetime={r.updated_at} title={dateTime(r.updated_at)}>{timeAgo(r.updated_at)}</time></td>
					</tr>
				{/each}
			</tbody>
		</table>
	</section>
{/if}

{#if previewing !== null && previewing >= 0}
	<PreviewDialog files={previewable} start={previewing} onClose={() => (previewing = null)} />
{/if}

<style>
	.head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		flex-wrap: wrap;
	}
	.title {
		font-size: 1.15rem;
		font-weight: 500;
		margin: 0 0 1rem;
	}
	.name a,
	.name button {
		color: var(--text-strong);
		overflow-wrap: anywhere;
		text-align: left;
	}
	.snippet {
		margin: 0.25rem 0 0 1.6rem;
		overflow-wrap: anywhere;
	}
	.snippet mark {
		background: none;
		color: var(--accent);
		font-weight: 600;
	}
	.location {
		color: var(--muted);
	}
	.num {
		text-align: right;
		white-space: nowrap;
	}
	.when {
		white-space: nowrap;
	}
</style>
