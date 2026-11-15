<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Group, type OidcClient, type OidcClientInput } from '#lib/api.ts';
	import AppForm from '#lib/components/admin/AppForm.svelte';
	import ClientCredentials from '#lib/components/admin/ClientCredentials.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	const id = page.params.id ?? '';

	let app = $state<OidcClient | null>(null);
	let issuer = $state('');
	let groups = $state<Group[]>([]);
	let secret = $state('');
	let error = $state<unknown>(null);
	let saved = $state(false);
	let busy = $state(false);

	async function load() {
		try {
			const [a, list, g] = await Promise.all([api.admin.app(id), api.admin.apps(), api.admin.groups()]);
			app = a;
			issuer = list.issuer;
			groups = g;
		} catch (e) {
			error = e;
		}
	}
	load();

	async function act(action: () => Promise<void>) {
		busy = true;
		error = null;
		saved = false;
		try {
			await action();
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	const save = (input: OidcClientInput) =>
		act(async () => {
			await api.admin.updateApp(id, input);
			saved = true;
			await load();
		});

	const rotate = () =>
		confirm('Een nieuw secret maken? Het oude werkt direct niet meer; pas de app daarna aan.') &&
		act(async () => {
			secret = await api.admin.rotateAppSecret(id);
		});

	const remove = () =>
		confirm(`App "${app?.name}" verwijderen? Gebruikers kunnen er dan niet meer mee inloggen.`) &&
		act(async () => {
			await api.admin.deleteApp(id);
			await goto('/admin/apps');
		});
</script>

<svelte:head>
	<title>{app?.name ?? 'App'} · Beheer · Cloud</title>
</svelte:head>

<p><a href="/admin/apps">← Apps</a></p>
<ErrorNotice {error} />

{#if app}
	<h1>{app.name}</h1>
	<div class="columns">
		<section class="panel">
			<h2>Instellingen</h2>
			{#if saved}<p class="flash flash--success" role="status">Opgeslagen.</p>{/if}
			{#key app}
				<AppForm {app} {groups} {busy} submitLabel="Opslaan" onSubmit={save} />
			{/key}
		</section>
		<div class="stack">
			<section class="panel">
				<h2>Gegevens voor de app</h2>
				<p class="muted small">{app.confidential ? 'Server-app met client-secret.' : 'Publieke app: gebruikt PKCE, geen secret.'}</p>
				<ClientCredentials {issuer} clientId={app.id} {secret} />
				{#if app.confidential}
					<button class="button" onclick={rotate} disabled={busy}>Nieuw client-secret maken</button>
				{/if}
			</section>
			<section class="panel">
				<h2>App verwijderen</h2>
				<p class="muted small">Alle tokens van deze app vervallen direct.</p>
				<button class="button" onclick={remove} disabled={busy}>Verwijderen</button>
			</section>
		</div>
	</div>
{/if}
