<script lang="ts">
	// Search results across everything the user can see.
	import Icon from '#lib/components/Icon.svelte';
	import { api, errorMessage, type SearchResult } from '#lib/api.ts';
	import { dateTime, formatBytes, previewKind, timeAgo } from '#lib/format.ts';
	import PreviewDialog from './PreviewDialog.svelte';

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
				const found = await api.drive.search(q);
				if (query.trim() === q) results = found;
			} catch (e) {
				error = errorMessage(e);
			}
		}, 250);
		return () => clearTimeout(timer);
	});
</script>

<h1 class="title">Zoekresultaten voor "{query.trim()}"</h1>
{#if error}
	<p class="flash flash--error" role="alert">{error}</p>
{:else if results === null}
	<p class="muted">Zoeken…</p>
{:else if results.length === 0}
	<p class="muted">Niets gevonden. Er wordt gezocht op naam, in je eigen bestanden, teammappen en wat met je gedeeld is.</p>
{:else}
	<section class="panel">
		<table class="table">
			<thead><tr><th>Naam</th><th>Locatie</th><th class="num">Grootte</th><th>Gewijzigd</th></tr></thead>
			<tbody>
				{#each results as r (r.id)}
					<tr>
						<td class="name">
							{#if r.kind === 'folder'}
								<a class="with-icon" href="/files/{r.id}"><Icon name="folder" />{r.name}</a>
							{:else if previewKind(r.content_type, r.name)}
								<button class="link-button with-icon" onclick={() => (previewing = previewable.indexOf(r))}><Icon name="file" />{r.name}</button>
							{:else}
								<a class="with-icon" href={api.drive.downloadURL(r.id)} download><Icon name="file" />{r.name}</a>
							{/if}
						</td>
						<td class="muted">
							{#if r.location_id}
								<a class="with-icon location" href="/files/{r.location_id}"><Icon name="folder" size={16} />{r.location}</a>
							{:else}
								{r.location}
							{/if}
						</td>
						<td class="num muted">{r.kind === 'file' ? formatBytes(r.size) : ''}</td>
						<td class="muted"><time datetime={r.updated_at} title={dateTime(r.updated_at)}>{timeAgo(r.updated_at)}</time></td>
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
	.location {
		color: var(--muted);
	}
	.num {
		text-align: right;
		white-space: nowrap;
	}
</style>
