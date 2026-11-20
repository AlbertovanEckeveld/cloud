<script lang="ts">
	// The assistant's own page: your conversations next to the current one
	// (ADR 0023, 0024). The same conversations open in the panel on every page.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Icon from '#lib/components/Icon.svelte';
	import AssistantChat from '#lib/components/assistant/AssistantChat.svelte';
	import { api, errorMessage, type Conversation } from '#lib/api.ts';
	import { timeAgo } from '#lib/format.ts';
	import { can } from '#lib/permissions.ts';

	let conversations = $state<Conversation[]>([]);
	let current = $state<string | null>(null);
	let error = $state('');

	const mayManage = $derived(can(page.data.user, 'assistant.provider.manage'));

	async function refresh() {
		try {
			conversations = await api.assistant.conversations();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	onMount(async () => {
		await refresh();
		current = conversations[0]?.id ?? null;
	});

	async function remove(c: Conversation) {
		if (!confirm(`Gesprek "${c.title}" verwijderen?`)) return;
		try {
			await api.assistant.deleteConversation(c.id);
			conversations = conversations.filter((x) => x.id !== c.id);
			if (current === c.id) current = null;
		} catch (e) {
			error = errorMessage(e);
		}
	}
</script>

<svelte:head>
	<title>Assistent · Cloud</title>
</svelte:head>

<div class="assistant">
	<aside class="threads" aria-label="Gesprekken">
		<button class="button button--primary" onclick={() => (current = null)}><span class="with-icon"><Icon name="plus" />Nieuw gesprek</span></button>
		{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
		<ul>
			{#each conversations as c (c.id)}
				<li class:active={c.id === current}>
					<button class="link-button thread" onclick={() => (current = c.id)}>
						<span class="thread__title">{c.title}</span>
						<span class="muted small">{timeAgo(c.updated_at)}</span>
					</button>
					<button class="icon-button" aria-label="Gesprek verwijderen" title="Verwijderen" onclick={() => remove(c)}><Icon name="trash" size={16} /></button>
				</li>
			{/each}
		</ul>
		<p class="muted small">Tip: met <kbd>Ctrl</kbd>+<kbd>J</kbd> open je de assistent op elke pagina.</p>
		{#if mayManage}<a class="small with-icon" href="/assistant/settings"><Icon name="settings" size={16} />AI-instellingen</a>{/if}
	</aside>

	<section>
		<AssistantChat bind:conversation={current} onConversation={refresh} />
	</section>
</div>

<style>
	.assistant {
		display: grid;
		grid-template-columns: 260px minmax(0, 1fr);
		gap: 2rem;
		align-items: start;
	}
	.threads {
		position: sticky;
		top: 1rem;
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.threads ul {
		list-style: none;
		padding: 0;
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		max-height: 60vh;
		overflow-y: auto;
	}
	.threads li {
		display: flex;
		align-items: center;
		border-radius: 10px;
	}
	.threads li.active {
		background: var(--surface-2);
	}
	.thread {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		padding: 0.45rem 0.6rem;
		color: var(--text);
		text-align: left;
	}
	.thread:hover {
		text-decoration: none;
	}
	.thread__title {
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	@media (max-width: 760px) {
		.assistant {
			grid-template-columns: minmax(0, 1fr);
			gap: 1rem;
		}
		.threads {
			position: static;
		}
		.threads ul {
			max-height: 9rem;
		}
	}
</style>
