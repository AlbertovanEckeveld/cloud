<script lang="ts">
	// A row of tabs that are links; the one matching the current page is marked.
	import { page } from '$app/state';

	let { tabs, label }: { tabs: { href: string; label: string }[]; label: string } = $props();
	const current = (href: string) => (page.url.pathname.startsWith(href) ? 'page' : undefined);
</script>

<nav class="tabs" aria-label={label}>
	{#each tabs as tab (tab.href)}
		<a href={tab.href} aria-current={current(tab.href)}>{tab.label}</a>
	{/each}
</nav>

<style>
	.tabs {
		display: flex;
		gap: 0.25rem;
		margin-bottom: 2rem;
		border-bottom: 1px solid var(--border);
		overflow-x: auto;
	}
	.tabs a {
		padding: 0.5rem 0.9rem;
		color: var(--muted);
		white-space: nowrap;
	}
	.tabs a:hover {
		color: var(--text-strong);
		text-decoration: none;
	}
	.tabs a[aria-current='page'] {
		color: var(--text-strong);
		box-shadow: inset 0 -2px 0 var(--accent);
	}
</style>
