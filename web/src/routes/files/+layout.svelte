<script lang="ts">
	// Files: a sidebar with "Nieuw", the sections and storage use, next to
	// the page that is open.
	import Icon from '#lib/components/Icon.svelte';
	import { onMount } from 'svelte';
	import { drive, refreshDrive, type Section } from '#lib/drive-state.svelte.ts';
	import { formatBytes } from '#lib/format.ts';
	import SearchResults from '#lib/components/drive/SearchResults.svelte';
	import { afterNavigate } from '$app/navigation';

	let { children } = $props();
	let menu: HTMLDetailsElement;
	let query = $state('');
	const searching = $derived(query.trim().length >= 2);

	// Opening a result (or any other page) ends the search.
	afterNavigate(() => (query = ''));

	onMount(refreshDrive);

	const is = (s: Section) =>
		typeof s === 'string' ? drive.section === s : typeof drive.section === 'object' && drive.section.team === s.team;
	const current = (s: Section) => (is(s) ? 'page' : undefined);

	function run(action: () => void) {
		menu.open = false;
		action();
	}

	const percent = $derived(
		drive.usage?.limit_bytes ? Math.min(100, (drive.usage.used_bytes / drive.usage.limit_bytes) * 100) : 0
	);
</script>

<div class="files">
	<aside class="sidebar" aria-label="Bestanden">
		<details class="new" bind:this={menu}>
			<summary class="button button--primary" class:disabled={!drive.create}><span class="with-icon"><Icon name="plus" />Nieuw</span></summary>
			{#if drive.create}
				<div class="new__menu" role="menu">
					<button role="menuitem" onclick={() => drive.create && run(drive.create.folder)}><span class="with-icon"><Icon name="folder" />Nieuwe map</span></button>
					<button role="menuitem" onclick={() => drive.create && run(drive.create.upload)}><span class="with-icon"><Icon name="upload" />Bestand uploaden</span></button>
				</div>
			{:else}
				<p class="new__menu muted small">Je kunt hier niets toevoegen.</p>
			{/if}
		</details>

		<nav class="nav">
			<a href="/files" aria-current={current('mine')}><Icon name="home" />Mijn bestanden</a>
			<a href="/files/shared" aria-current={current('shared')}><Icon name="share" />Gedeeld met mij</a>
			{#if drive.teams.length > 0}
				<span class="nav__heading">Teammappen</span>
				{#each drive.teams as t (t.id)}
					<a href="/files/{t.id}" aria-current={current({ team: t.id })}><Icon name="users" />{t.name}</a>
				{/each}
			{/if}
			<a class="nav__trash" href="/files/activity" aria-current={current('activity')}><Icon name="history" />Activiteit</a>
			<a href="/files/trash" aria-current={current('trash')}><Icon name="trash" />Prullenbak</a>
		</nav>

		{#if drive.usage}
			<div class="usage small">
				<span class="with-icon"><Icon name="cloud" />Opslag</span>
				{#if drive.usage.limit_bytes !== null}
					<progress max="100" value={percent}></progress>
					<span class="muted">{formatBytes(drive.usage.used_bytes)} van {formatBytes(drive.usage.limit_bytes)} gebruikt</span>
				{:else}
					<span class="muted">{formatBytes(drive.usage.used_bytes)} gebruikt</span>
				{/if}
			</div>
		{/if}
		{#if drive.error}<p class="small muted">{drive.error}</p>{/if}
	</aside>

	<div class="content">
		<form class="search" role="search" onsubmit={(e) => e.preventDefault()}>
			<Icon name="search" />
			<input
				type="search"
				bind:value={query}
				placeholder="Zoeken in bestanden"
				aria-label="Zoeken in bestanden"
				autocomplete="off"
				maxlength="200"
				onkeydown={(e) => e.key === 'Escape' && (query = '')}
			/>
		</form>
		{#if searching}
			<SearchResults {query} />
		{:else}
			{@render children()}
		{/if}
	</div>
</div>

<style>
	.files {
		display: grid;
		grid-template-columns: 220px minmax(0, 1fr);
		gap: 2rem;
		align-items: start;
	}
	.search {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		padding: 0 1.1rem;
		margin-bottom: 1.25rem;
		background: var(--surface-2);
		border: 1px solid transparent;
		border-radius: 999px;
		color: var(--muted);
	}
	.search:focus-within {
		border-color: var(--accent);
		background: var(--surface);
	}
	.search input {
		flex: 1;
		border: 0;
		background: none;
		padding: 0.75rem 0;
		color: var(--text);
		outline: none;
	}
	.sidebar {
		position: sticky;
		top: 1rem;
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
	}
	.new {
		position: relative;
	}
	.new summary {
		list-style: none;
		cursor: pointer;
		padding: 0.75rem 1.4rem;
		font-size: 1rem;
	}
	.new summary::-webkit-details-marker {
		display: none;
	}
	.new summary.disabled {
		opacity: 0.6;
	}
	.new__menu {
		position: absolute;
		z-index: 10;
		top: calc(100% + 0.4rem);
		left: 0;
		min-width: 210px;
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.4rem;
		margin: 0;
		box-shadow: 0 8px 24px rgb(0 0 0 / 0.35);
	}
	.new__menu button {
		font: inherit;
		text-align: left;
		background: none;
		border: 0;
		color: var(--text);
		padding: 0.55rem 0.75rem;
		border-radius: 8px;
		cursor: pointer;
	}
	.new__menu button:hover {
		background: var(--surface-2);
	}
	.nav {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.nav a {
		display: flex;
		align-items: center;
		gap: 0.65rem;
		color: var(--text);
		padding: 0.5rem 0.9rem;
		border-radius: 999px;
		overflow-wrap: anywhere;
	}
	.nav a:hover {
		background: var(--surface-2);
		text-decoration: none;
	}
	.nav a[aria-current='page'] {
		background: var(--surface-2);
		color: var(--text-strong);
		box-shadow: inset 3px 0 0 var(--accent);
	}
	.nav__heading {
		margin: 0.75rem 0 0.25rem 0.9rem;
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--muted);
	}
	.nav__trash {
		margin-top: 0.75rem;
	}
	.usage {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		padding: 0 0.9rem;
	}
	.usage progress {
		width: 100%;
	}
	@media (max-width: 760px) {
		.files {
			grid-template-columns: 1fr;
			gap: 1rem;
		}
		.search {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		padding: 0 1.1rem;
		margin-bottom: 1.25rem;
		background: var(--surface-2);
		border: 1px solid transparent;
		border-radius: 999px;
		color: var(--muted);
	}
	.search:focus-within {
		border-color: var(--accent);
		background: var(--surface);
	}
	.search input {
		flex: 1;
		border: 0;
		background: none;
		padding: 0.75rem 0;
		color: var(--text);
		outline: none;
	}
	.sidebar {
			position: static;
		}
		.nav {
			flex-direction: row;
			flex-wrap: wrap;
		}
		.nav__heading {
			width: 100%;
		}
	}
</style>
