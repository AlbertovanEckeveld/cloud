<script lang="ts">
	import { can, canAnyIn } from '#lib/permissions.ts';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Start · Cloud</title>
</svelte:head>

{#if data.user}
	<h1>Welkom, {data.user.display_name}</h1>
	<p class="muted">Agenda, foto's en meer volgen in de komende fases.</p>

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
