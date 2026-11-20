<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Group, type OidcClient, type OidcClientInput } from '#lib/api.ts';
	import AppForm from '#lib/components/admin/AppForm.svelte';
	import ClientCredentials from '#lib/components/admin/ClientCredentials.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	let issuer = $state('');
	let apps = $state<OidcClient[] | null>(null);
	let groups = $state<Group[]>([]);
	let error = $state<unknown>(null);
	let creating = $state(false);
	let busy = $state(false);
	let created = $state<{ client: OidcClient; secret: string } | null>(null);

	async function load() {
		try {
			const [list, g] = await Promise.all([api.admin.apps(), api.admin.groups()]);
			issuer = list.issuer;
			apps = list.clients;
			groups = g;
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function create(input: OidcClientInput) {
		busy = true;
		error = null;
		try {
			created = await api.admin.createApp(input);
			creating = false;
			await load();
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Apps · Identity · Cloud</title>
</svelte:head>

<div class="page-header">
	<h1>Apps</h1>
	<button class="button button--primary" onclick={() => ((creating = !creating), (created = null))}>
		{creating ? 'Annuleren' : 'App toevoegen'}
	</button>
</div>
<p class="muted">
	Apps die gebruikers laten inloggen met hun Cloud-account (OpenID Connect), zoals Grafana of Home
	Assistant.
</p>

<ErrorNotice {error} />

{#if created}
	<section class="panel created">
		<h2>{created.client.name} is toegevoegd</h2>
		<p class="muted small">Vul deze gegevens in bij de OIDC-instellingen van de app.</p>
		<ClientCredentials {issuer} clientId={created.client.id} secret={created.secret} />
	</section>
{/if}

{#if creating}
	<section class="panel create">
		<AppForm {groups} {busy} submitLabel="Toevoegen" onSubmit={create} />
	</section>
{/if}

{#if apps && apps.length > 0}
	<div class="panel table-wrap">
		<table class="table">
			<thead><tr><th>App</th><th>Soort</th><th>Toegang</th><th>Client-ID</th></tr></thead>
			<tbody>
				{#each apps as a (a.id)}
					<tr>
						<td><a href="/identity/apps/{a.id}">{a.name}</a></td>
						<td>{a.confidential ? 'Server-app' : 'Publiek (PKCE)'}</td>
						<td>{a.allowed_group ? `Groep ${a.allowed_group.name}` : 'Alle gebruikers'}</td>
						<td><code>{a.id}</code></td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{:else if apps}
	<p class="muted">Er zijn nog geen apps.</p>
{/if}

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		flex-wrap: wrap;
	}
	.created,
	.create {
		max-width: 640px;
		margin-bottom: 1.5rem;
	}
</style>
