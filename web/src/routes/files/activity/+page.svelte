<script lang="ts">
	// What happened in your files lately, and through which channel: the
	// web app, the desktop app (sync), WebDAV or the document editor.
	import { onMount } from 'svelte';
	import { api, type ActivityItem } from '#lib/api.ts';
	import { dateTime } from '#lib/format.ts';
	import Icon from '#lib/components/Icon.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import { drive } from '#lib/drive-state.svelte.ts';

	drive.section = 'activity';

	let items = $state<ActivityItem[] | null>(null);
	let error = $state<unknown>(null);

	const verbs: Record<ActivityItem['action'], string> = {
		created: 'heeft toegevoegd',
		updated: 'heeft gewijzigd',
		renamed: 'heeft hernoemd tot',
		moved: 'heeft verplaatst',
		trashed: 'heeft in de prullenbak gezet',
		restored: 'heeft teruggezet',
		deleted: ''
	};
	const channels: Record<ActivityItem['via'], string> = {
		'': '',
		web: 'via de webapp',
		desktop: 'via de desktop-app',
		webdav: 'via WebDAV',
		editor: 'via de documenteditor'
	};

	const day = (iso: string) => new Date(iso).toLocaleDateString('nl-NL', { weekday: 'long', day: 'numeric', month: 'long' });
	const time = (iso: string) => new Date(iso).toLocaleTimeString('nl-NL', { hour: '2-digit', minute: '2-digit' });

	async function load() {
		try {
			items = await api.drive.activity();
		} catch (e) {
			error = e;
		}
	}
	onMount(() => {
		load();
		const timer = setInterval(load, 15_000); // sync activity keeps coming in
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Activiteit · Cloud</title>
</svelte:head>

<h1>Activiteit</h1>
<p class="muted">Wat er de laatste 30 dagen in je bestanden, teammappen en gedeelde mappen veranderde, en via welk kanaal.</p>
<ErrorNotice {error} />

{#if items && items.length === 0}
	<p class="muted">Nog geen activiteit.</p>
{:else if items}
	<section class="panel">
		<ul class="list">
			{#each items as it, i (it.node_id + it.at + it.action)}
				{#if i === 0 || day(it.at) !== day(items[i - 1].at)}
					<li class="day">{day(it.at)}</li>
				{/if}
				<li>
					<span class="with-icon">
						<Icon name={it.kind === 'folder' ? 'folder' : 'file'} />
						<span>
							<span class="muted small"><time datetime={it.at} title={dateTime(it.at)}>{time(it.at)}</time></span>
							{#if it.action === 'deleted'}
								<strong>{it.name}</strong> is definitief verwijderd
							{:else}
								{it.actor_name || 'Iemand'}
								{verbs[it.action]}
								{#if it.present && it.kind === 'folder'}
									<a href="/files/{it.node_id}">{it.name}</a>
								{:else}
									<strong>{it.name}</strong>
								{/if}
							{/if}
							<span class="muted small">{channels[it.via]}</span>
						</span>
					</span>
				</li>
			{/each}
		</ul>
	</section>
{/if}

<style>
	.day {
		font-size: 0.8rem;
		text-transform: capitalize;
		color: var(--muted);
		border-bottom: 0;
		padding-top: 1rem;
	}
</style>
