<script lang="ts">
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Start · Cloud</title>
</svelte:head>

{#if data.user}
	<h1>Welkom, {data.user.display_name}</h1>
	<p class="muted">Bestanden, agenda, foto's en meer volgen in de komende fases.</p>

	<div class="cards">
		<a class="panel card" href="/account">
			<h2>Account</h2>
			<p class="muted">Wachtwoord wijzigen en je recente activiteit bekijken.</p>
		</a>
		{#if data.user.is_admin}
			<a class="panel card" href="/admin">
				<h2>Beheer</h2>
				<p class="muted">Gebruikers, groepen, audit-log en systeemstatus.</p>
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
