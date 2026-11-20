<script lang="ts">
	import '../app.css';
	import { goto, invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { can, canAnyIn } from '#lib/permissions.ts';
	import Icon from '#lib/components/Icon.svelte';
	import AssistantPanel from '#lib/components/assistant/AssistantPanel.svelte';
	import { loadAssistantStatus, toggleAssistant } from '#lib/assistant-state.svelte.ts';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();

	async function logout() {
		try {
			await api.logout();
		} finally {
			await invalidateAll();
			await goto('/login');
		}
	}

	const current = (path: string) => (page.url.pathname === path ? 'page' : undefined);

	$effect(() => {
		if (data.user) loadAssistantStatus(api.assistant.status);
	});

	// Ctrl+J (Cmd+J on a Mac) opens the assistant on every page.
	function keys(e: KeyboardEvent) {
		if (data.user && (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'j') {
			e.preventDefault();
			toggleAssistant();
		}
	}
</script>

<a class="skip-link" href="#main">Naar inhoud</a>

<header class="site-header">
	<div class="container site-header__inner">
		<a class="brand" href="/">Cloud</a>
		{#if data.user}
			<nav class="nav" aria-label="Hoofdmenu">
				<a href="/" aria-current={current('/')}>Start</a>
				<a href="/files" aria-current={page.url.pathname.startsWith('/files') ? 'page' : undefined}>Bestanden</a>
				<a href="/assistant" aria-current={page.url.pathname.startsWith('/assistant') ? 'page' : undefined}>Assistent</a>
				{#if canAnyIn(data.user, 'identity')}
					<a href="/identity" aria-current={page.url.pathname.startsWith('/identity') ? 'page' : undefined}>Identity</a>
				{/if}
				{#if can(data.user, 'system.status.view')}
					<a href="/system" aria-current={current('/system')}>Systeem</a>
				{/if}
			</nav>
			<div class="site-header__actions">
				<button class="icon-button" aria-label="Assistent openen" title="Assistent (Ctrl+J)" onclick={toggleAssistant}><Icon name="spark" /></button>
				<a href="/account" aria-current={current('/account')}>{data.user.display_name}</a>
				<button class="button" onclick={logout}>Uitloggen</button>
			</div>
		{/if}
	</div>
</header>

<svelte:window onkeydown={keys} />

<main id="main" class="container">
	{@render children()}
</main>

{#if data.user}<AssistantPanel />{/if}

<style>
	.site-header {
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.site-header__inner {
		display: flex;
		align-items: center;
		gap: 1.5rem;
		flex-wrap: wrap;
		padding-block: 0.75rem;
	}
	.brand {
		font-weight: 700;
		color: var(--text-strong);
	}
	.brand:hover {
		text-decoration: none;
	}
	.nav {
		display: flex;
		gap: 1.25rem;
	}
	.nav a,
	.site-header__actions a {
		color: var(--muted);
	}
	.nav a:hover,
	.nav a[aria-current='page'],
	.site-header__actions a:hover,
	.site-header__actions a[aria-current='page'] {
		color: var(--text-strong);
		text-decoration: none;
	}
	.nav a[aria-current='page'] {
		box-shadow: inset 0 -2px 0 var(--accent);
	}
	.site-header__actions {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 1rem;
	}
	main {
		padding-block: 2.5rem 4rem;
	}
	@media (max-width: 600px) {
		.site-header__inner {
			gap: 0.5rem 1rem;
		}
		.site-header__actions a {
			max-width: 9rem;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}
		main {
			padding-block: 1.25rem 3rem;
		}
	}
</style>
