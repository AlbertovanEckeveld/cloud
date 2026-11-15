<script lang="ts">
	import { page } from '$app/state';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	const tabs = [
		{ href: '/admin/users', label: 'Gebruikers' },
		{ href: '/admin/groups', label: 'Groepen' },
		{ href: '/admin/audit', label: 'Audit-log' },
		{ href: '/admin/system', label: 'Systeem' }
	];
	const current = (href: string) => (page.url.pathname.startsWith(href) ? 'page' : undefined);
</script>

<nav class="tabs" aria-label="Beheer">
	{#each tabs as tab (tab.href)}
		<a href={tab.href} aria-current={current(tab.href)}>{tab.label}</a>
	{/each}
</nav>

{@render children()}

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
