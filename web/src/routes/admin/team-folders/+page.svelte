<script lang="ts">
	// Team folders: folders for groups that belong to no single person.
	// Administrators decide which groups have access; they do not see the
	// contents themselves (ADR 0011).
	import { onMount } from 'svelte';
	import { api, type Group, type Permission, type TeamFolder } from '#lib/api.ts';
	import { dateTime, formatBytes, permissionLabel } from '#lib/format.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	const levels: Permission[] = ['view', 'edit', 'manage'];

	let folders = $state<TeamFolder[] | null>(null);
	let groups = $state<Group[]>([]);
	let error = $state<unknown>(null);
	let name = $state('');
	let busy = $state(false);
	// Per team folder: the form state for adding a group, renaming and the quota.
	let addGroup = $state<Record<string, { group: string; permission: Permission }>>({});
	let names = $state<Record<string, string>>({});
	let quotas = $state<Record<string, string>>({});

	function remember(list: TeamFolder[]) {
		folders = list;
		for (const t of list) {
			addGroup[t.id] ??= { group: '', permission: 'edit' };
			names[t.id] = t.name;
			quotas[t.id] = t.usage.limit_bytes === null ? '' : String(t.usage.limit_bytes / 1024 ** 3);
		}
	}

	async function load() {
		try {
			const [list, g] = await Promise.all([api.admin.teamFolders(), api.admin.groups()]);
			groups = g;
			remember(list);
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function act(action: () => Promise<unknown>) {
		busy = true;
		error = null;
		try {
			await action();
			remember(await api.admin.teamFolders());
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	function create(e: SubmitEvent) {
		e.preventDefault();
		return act(async () => {
			await api.admin.createTeamFolder(name);
			name = '';
		});
	}

	function rename(e: SubmitEvent, t: TeamFolder) {
		e.preventDefault();
		return act(() => api.admin.renameTeamFolder(t.id, names[t.id]));
	}

	function saveQuota(e: SubmitEvent, t: TeamFolder) {
		e.preventDefault();
		const raw = quotas[t.id].trim();
		const gb = raw === '' ? null : Number(raw.replace(',', '.'));
		if (gb !== null && (!Number.isFinite(gb) || gb < 0)) {
			error = new Error('Vul een aantal GB in, of laat het veld leeg voor geen limiet.');
			return;
		}
		return act(() => api.admin.setTeamFolderQuota(t.id, gb === null ? null : Math.round(gb * 1024 ** 3)));
	}

	function add(e: SubmitEvent, t: TeamFolder) {
		e.preventDefault();
		const a = addGroup[t.id];
		if (!a.group) return;
		return act(async () => {
			await api.admin.setTeamFolderGroup(t.id, a.group, a.permission);
			a.group = '';
		});
	}

	const available = (t: TeamFolder) => groups.filter((g) => !t.groups.some((s) => s.subject.id === g.id));

	const remove = (t: TeamFolder) =>
		confirm(`Teammap "${t.name}" opheffen? De leden kunnen er dan niet meer bij. Je kunt hem 30 dagen terugzetten.`) &&
		act(() => api.admin.deleteTeamFolder(t.id));
	const purge = (t: TeamFolder) =>
		confirm(`Teammap "${t.name}" met alle bestanden definitief verwijderen? Dit kan niet ongedaan worden gemaakt.`) &&
		act(() => api.admin.purgeTeamFolder(t.id));
</script>

<svelte:head>
	<title>Teammappen · Beheer · Cloud</title>
</svelte:head>

<h1>Teammappen</h1>
<p class="muted">
	Een teammap is van niemand persoonlijk: de groepen die je toevoegt, delen hem. Wat erin staat, telt voor de
	opslag van de teammap. Als beheerder regel je de toegang, maar zie je de inhoud alleen als je zelf in een van de
	groepen zit.
</p>

<ErrorNotice {error} />

<form class="panel add-folder" onsubmit={create}>
	<label class="visually-hidden" for="team-name">Naam van de nieuwe teammap</label>
	<input id="team-name" bind:value={name} required maxlength="255" placeholder="Naam van de nieuwe teammap" />
	<button class="button button--primary" disabled={busy}>Aanmaken</button>
</form>

{#if folders && folders.length === 0}
	<p class="muted">Er zijn nog geen teammappen.</p>
{/if}

{#each folders ?? [] as t (t.id)}
	<section class="panel team" class:team--deleted={t.deleted_at} aria-labelledby="team-{t.id}">
		<div class="team__head">
			<h2 id="team-{t.id}">👥 {t.name}</h2>
			{#if t.deleted_at}
				<span class="badge badge--error">Opgeheven {dateTime(t.deleted_at)}</span>
			{/if}
			<span class="muted small">
				{formatBytes(t.usage.used_bytes)} gebruikt{#if t.usage.limit_bytes !== null}&nbsp;van {formatBytes(t.usage.limit_bytes)}{/if}
			</span>
		</div>

		{#if t.deleted_at}
			<p class="muted">De leden kunnen er niet meer bij. Na 30 dagen wordt de teammap automatisch definitief verwijderd.</p>
			<div class="dialog-actions">
				<button class="button" disabled={busy} onclick={() => act(() => api.admin.restoreTeamFolder(t.id))}>Terugzetten</button>
				<button class="button" disabled={busy} onclick={() => purge(t)}>Definitief verwijderen</button>
			</div>
		{:else}
			<h3>Groepen</h3>
			{#if t.groups.length === 0}
				<p class="muted small">Nog geen groepen: niemand kan de teammap openen.</p>
			{:else}
				<ul class="list">
					{#each t.groups as g (g.subject.id)}
						<li>
							<span>{g.name}</span>
							<span class="row">
								<label class="visually-hidden" for="lvl-{t.id}-{g.subject.id}">Recht van {g.name}</label>
								<select
									id="lvl-{t.id}-{g.subject.id}"
									value={g.permission}
									disabled={busy}
									onchange={(e) => act(() => api.admin.setTeamFolderGroup(t.id, g.subject.id, e.currentTarget.value as Permission))}
								>
									{#each levels as l (l)}<option value={l}>{permissionLabel(l)}</option>{/each}
								</select>
								<button class="link-button" disabled={busy} onclick={() => act(() => api.admin.removeTeamFolderGroup(t.id, g.subject.id))}>
									Verwijderen
								</button>
							</span>
						</li>
					{/each}
				</ul>
			{/if}
			{#if available(t).length > 0}
				<form class="add" onsubmit={(e) => add(e, t)}>
					<label class="visually-hidden" for="grp-{t.id}">Groep toevoegen</label>
					<select id="grp-{t.id}" bind:value={addGroup[t.id].group}>
						<option value="">Groep kiezen…</option>
						{#each available(t) as g (g.id)}<option value={g.id}>{g.name}</option>{/each}
					</select>
					<label class="visually-hidden" for="perm-{t.id}">Recht</label>
					<select id="perm-{t.id}" bind:value={addGroup[t.id].permission}>
						{#each levels as l (l)}<option value={l}>{permissionLabel(l)}</option>{/each}
					</select>
					<button class="button" disabled={busy || !addGroup[t.id].group}>Toevoegen</button>
				</form>
			{/if}

			<h3>Instellingen</h3>
			<div class="settings">
				<form class="add" onsubmit={(e) => rename(e, t)}>
					<label class="visually-hidden" for="name-{t.id}">Naam</label>
					<input id="name-{t.id}" bind:value={names[t.id]} required maxlength="255" />
					<button class="button" disabled={busy || names[t.id] === t.name}>Hernoemen</button>
				</form>
				<form class="add" onsubmit={(e) => saveQuota(e, t)}>
					<input bind:value={quotas[t.id]} inputmode="decimal" placeholder="Geen limiet" aria-label="Limiet in GB" />
					<span class="muted">GB</span>
					<button class="button" disabled={busy}>Opslaan</button>
				</form>
			</div>
			<div class="dialog-actions">
				<button class="button" disabled={busy} onclick={() => remove(t)}>Opheffen</button>
			</div>
		{/if}
	</section>
{/each}

<style>
	.add-folder {
		display: flex;
		gap: 0.5rem;
		margin-bottom: 1.5rem;
	}
	.add-folder input {
		flex: 1;
	}
	.team {
		margin-bottom: 1rem;
	}
	.team--deleted {
		opacity: 0.8;
	}
	.team__head {
		display: flex;
		align-items: baseline;
		gap: 0.75rem;
		flex-wrap: wrap;
	}
	.team__head h2 {
		margin: 0;
	}
	.team h3 {
		margin-top: 1.25rem;
	}
	.list li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.settings {
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
	}
	.settings .add {
		align-items: center;
		margin-bottom: 0;
	}
</style>
