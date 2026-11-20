<script lang="ts">
	import { can, canAnyIn } from '#lib/permissions.ts';
	import Icon from '#lib/components/Icon.svelte';
	import { askAssistant, assistant } from '#lib/assistant-state.svelte.ts';

	let question = $state('');
	function ask(e: SubmitEvent) {
		e.preventDefault();
		if (question.trim()) askAssistant(question.trim());
		question = '';
	}
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Start · Cloud</title>
</svelte:head>

{#if data.user}
	<h1>Welkom, {data.user.display_name}</h1>
	<p class="muted">Agenda, foto's en meer volgen in de komende fases.</p>

	{#if assistant.available}
		<form class="ask" onsubmit={ask}>
			<Icon name="spark" />
			<input bind:value={question} maxlength="4000" placeholder="Vraag de assistent iets over je bestanden…" aria-label="Vraag aan de assistent" />
			<button class="button button--primary" disabled={!question.trim()}>Vragen</button>
		</form>
	{/if}

	<div class="cards">
		<a class="panel card" href="/files">
			<h2>Bestanden</h2>
			<p class="muted">Je bestanden en mappen: uploaden, ordenen en downloaden.</p>
		</a>
		<a class="panel card" href="/assistant">
			<h2>Assistent</h2>
			<p class="muted">Vragen over je bestanden, met antwoorden en bronnen.</p>
		</a>
		<a class="panel card" href="/account">
			<h2>Account</h2>
			<p class="muted">Wachtwoord wijzigen en je recente activiteit bekijken.</p>
		</a>
		{#if canAnyIn(data.user, 'identity')}
			<a class="panel card" href="/identity">
				<h2>Identity</h2>
				<p class="muted">Gebruikers, groepen, rollen en rechten, apps en de audit-log.</p>
			</a>
		{/if}
		{#if canAnyIn(data.user, 'drive')}
			<a class="panel card" href="/files/settings">
				<h2>Drive-instellingen</h2>
				<p class="muted">Teammappen en opslag per gebruiker.</p>
			</a>
		{/if}
		{#if can(data.user, 'system.status.view')}
			<a class="panel card" href="/system">
				<h2>Systeem</h2>
				<p class="muted">Status van database, opslag en sites.</p>
			</a>
		{/if}
	</div>
{/if}

<style>
	.ask {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		margin-top: 1.5rem;
		padding: 0.4rem 0.4rem 0.4rem 1rem;
		background: var(--surface-2);
		border-radius: 999px;
		max-width: 720px;
	}
	.ask input {
		border: 0;
		background: none;
	}
	.cards {
		display: grid;
		gap: 1rem;
		grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
		margin-top: 2rem;
	}
	.card {
		color: inherit;
	}
	.card:hover {
		text-decoration: none;
		border-color: var(--accent);
	}
	.card p {
		margin: 0;
	}
</style>
