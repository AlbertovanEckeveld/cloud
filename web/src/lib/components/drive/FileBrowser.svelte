<script lang="ts">
	// A folder's contents with everything you can do with them: create
	// folders, upload (button or drag and drop), download, rename, move, move
	// to the trash, share and see versions. Actions depend on the user's
	// permission; the server checks it again for every request.
	import { onMount } from 'svelte';
	import { api, ApiError, errorMessage, type DriveNode, type FolderView, type Usage } from '#lib/api.ts';
	import { allows, dateTime, formatBytes, permissionLabel, timeAgo, uniqueName } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';
	import MoveDialog from './MoveDialog.svelte';
	import ShareDialog from './ShareDialog.svelte';
	import VersionsDialog from './VersionsDialog.svelte';

	let { folderId }: { folderId: string | null } = $props();

	type UploadState = { key: number; name: string; progress: number; error: string | null };

	let view = $state<FolderView | null>(null);
	let teams = $state<DriveNode[]>([]);
	let usage = $state<Usage | null>(null);
	let error = $state<unknown>(null);
	let uploads = $state<UploadState[]>([]);
	let newFolder = $state<string | null>(null);
	let renaming = $state<{ id: string; name: string } | null>(null);
	let moving = $state<DriveNode | null>(null);
	let sharing = $state<DriveNode | null>(null);
	let history = $state<DriveNode | null>(null);
	let dragging = $state(false);
	let uploadKey = 0;

	async function load() {
		try {
			[view, usage, teams] = await Promise.all([
				api.drive.folder(folderId),
				api.drive.usage(),
				folderId === null ? api.drive.teamFolders() : Promise.resolve([])
			]);
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function act(action: () => Promise<unknown>) {
		error = null;
		try {
			await action();
			await load();
		} catch (e) {
			error = e;
		}
	}

	function createFolder(e: SubmitEvent) {
		e.preventDefault();
		const name = newFolder ?? '';
		return act(async () => {
			await api.drive.createFolder(folderId, name);
			newFolder = null;
		});
	}

	function rename(e: SubmitEvent) {
		e.preventDefault();
		const r = renaming;
		if (!r) return;
		return act(async () => {
			await api.drive.update(r.id, { name: r.name });
			renaming = null;
		});
	}

	const move = (node: DriveNode, target: string | null) =>
		act(async () => {
			await api.drive.update(node.id, { parent_id: target ?? 'root' });
			moving = null;
		});

	const trash = (node: DriveNode) => act(() => api.drive.trash(node.id));

	async function uploadOne(file: File) {
		const state: UploadState = { key: uploadKey++, name: file.name, progress: 0, error: null };
		uploads = [...uploads, state];
		const item = () => uploads.find((u) => u.key === state.key)!;
		const progress = (p: number) => (item().progress = p);
		try {
			await api.drive.upload(folderId, file, false, progress);
		} catch (e) {
			if (!(e instanceof ApiError && e.code === 'name_taken')) {
				item().error = errorMessage(e);
				return;
			}
			// The name exists: replace it (a new version), or keep both.
			if (confirm(`"${file.name}" bestaat al. Vervangen door de nieuwe versie?\n\nAnnuleren = beide bewaren.`)) {
				try {
					await api.drive.upload(folderId, file, true, progress);
				} catch (e2) {
					item().error = errorMessage(e2);
					return;
				}
			} else {
				const taken = new Set(view?.children.map((c) => c.name));
				const renamed = new File([file], uniqueName(file.name, taken), { type: file.type });
				try {
					await api.drive.upload(folderId, renamed, false, progress);
				} catch (e2) {
					item().error = errorMessage(e2);
					return;
				}
			}
		}
		uploads = uploads.filter((u) => u.key !== state.key);
	}

	async function uploadAll(files: FileList | File[]) {
		await Promise.all([...files].map(uploadOne));
		await load();
	}

	function pick(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		if (input.files) uploadAll([...input.files]);
		input.value = '';
	}

	function drop(e: DragEvent) {
		e.preventDefault();
		dragging = false;
		if (canEdit && e.dataTransfer?.files.length) uploadAll(e.dataTransfer.files);
	}

	// The top level is the user's own; elsewhere the folder says what they may do.
	const canEdit = $derived(!view?.folder || allows(view.folder.permission, 'edit'));
	const shareRoot = $derived(view?.shared ? (view.path[0] ?? null) : null);
	const trashHref = $derived(view?.team ? `/files/trash?team=${view.team.id}` : '/files/trash');

	const percent = (u: Usage) => (u.limit_bytes ? Math.min(100, (u.used_bytes / u.limit_bytes) * 100) : 0);
</script>

<div class="toolbar">
	<nav class="crumbs" aria-label="Map">
		{#if view?.team}
			<a href="/files">Teammappen</a>
		{:else}
			<a href={view?.shared ? '/files/shared' : '/files'}>{view?.shared ? 'Gedeeld met mij' : 'Bestanden'}</a>
		{/if}
		{#each view?.path ?? [] as c (c.id)}
			<span class="muted">/</span> <a href="/files/{c.id}">{c.name}</a>
		{/each}
		{#if view?.folder && view.shared}<span class="badge">{permissionLabel(view.folder.permission)}</span>{/if}
	</nav>
	<div class="actions">
		{#if canEdit}
			<button class="button" onclick={() => (newFolder = '')}>Nieuwe map</button>
			<label class="button button--primary">
				Uploaden
				<input type="file" multiple hidden onchange={pick} />
			</label>
		{/if}
		<a class="button" href="/files/shared">Gedeeld met mij</a>
		<a class="button" href={trashHref}>Prullenbak</a>
	</div>
</div>

<ErrorNotice {error} />

{#if teams.length > 0}
	<section class="panel teams" aria-labelledby="teams-title">
		<h2 id="teams-title">Teammappen</h2>
		<ul class="team-list">
			{#each teams as t (t.id)}
				<li>
					<a href="/files/{t.id}">👥 {t.name}</a>
					<span class="badge">{permissionLabel(t.permission)}</span>
				</li>
			{/each}
		</ul>
	</section>
{/if}

{#each uploads as u (u.key)}
	<div class="upload" class:upload--error={u.error}>
		<span>{u.name}</span>
		{#if u.error}
			<span>{u.error}</span>
			<button class="link" onclick={() => (uploads = uploads.filter((x) => x.key !== u.key))}>Sluiten</button>
		{:else}
			<progress max="1" value={u.progress}></progress>
		{/if}
	</div>
{/each}

<section
	class="panel drop"
	class:drop--active={dragging}
	ondragover={(e) => canEdit && (e.preventDefault(), (dragging = true))}
	ondragleave={() => (dragging = false)}
	ondrop={drop}
	aria-label="Bestanden in deze map"
>
	{#if newFolder !== null}
		<form class="add" onsubmit={createFolder}>
			<!-- svelte-ignore a11y_autofocus -->
			<input bind:value={newFolder} placeholder="Naam van de map" maxlength="255" autofocus required />
			<button class="button button--primary">Aanmaken</button>
			<button type="button" class="button" onclick={() => (newFolder = null)}>Annuleren</button>
		</form>
	{/if}

	{#if view && view.children.length === 0}
		<p class="empty muted">Deze map is leeg.{canEdit ? ' Sleep bestanden hierheen of gebruik Uploaden.' : ''}</p>
	{:else if view}
		<table class="table">
			<thead><tr><th>Naam</th><th class="num">Grootte</th><th>Gewijzigd</th><th><span class="visually-hidden">Acties</span></th></tr></thead>
			<tbody>
				{#each view.children as n (n.id)}
					<tr>
						<td class="name">
							{#if renaming?.id === n.id}
								<form class="add" onsubmit={rename}>
									<!-- svelte-ignore a11y_autofocus -->
									<input bind:value={renaming.name} maxlength="255" required autofocus />
									<button class="button button--primary">Opslaan</button>
									<button type="button" class="button" onclick={() => (renaming = null)}>Annuleren</button>
								</form>
							{:else if n.kind === 'folder'}
								<a href="/files/{n.id}">📁 {n.name}</a>
							{:else}
								<a href={api.drive.downloadURL(n.id)} download>📄 {n.name}</a>
							{/if}
							{#if n.shared}<span class="badge" title="Gedeeld met anderen">👥 Gedeeld</span>{/if}
						</td>
						<td class="num muted">{n.kind === 'file' ? formatBytes(n.size) : ''}</td>
						<td class="muted"><time datetime={n.updated_at} title={dateTime(n.updated_at)}>{timeAgo(n.updated_at)}</time></td>
						<td class="row-actions">
							{#if allows(n.permission, 'manage')}
								<button class="link" onclick={() => (sharing = n)}>Delen</button>
							{/if}
							{#if n.kind === 'file'}
								<button class="link" onclick={() => (history = n)}>Versies</button>
							{/if}
							{#if allows(n.permission, 'edit')}
								<button class="link" onclick={() => (renaming = { id: n.id, name: n.name })}>Hernoemen</button>
								<button class="link" onclick={() => (moving = n)}>Verplaatsen</button>
								<button class="link" onclick={() => trash(n)}>Verwijderen</button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>

{#if usage && !view?.shared}
	<p class="usage muted small">
		{#if usage.limit_bytes !== null}
			<progress max="100" value={percent(usage)}></progress>
			{formatBytes(usage.used_bytes)} van {formatBytes(usage.limit_bytes)} gebruikt
		{:else}
			{formatBytes(usage.used_bytes)} gebruikt
		{/if}
	</p>
{/if}

{#if moving}
	<MoveDialog
		node={moving}
		root={shareRoot}
		onMove={(target) => moving && move(moving, target)}
		onClose={() => (moving = null)}
	/>
{/if}
{#if sharing}
	<ShareDialog node={sharing} onClose={() => ((sharing = null), load())} />
{/if}
{#if history}
	<VersionsDialog node={history} onChanged={load} onClose={() => (history = null)} />
{/if}

<style>
	.toolbar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		flex-wrap: wrap;
		margin-bottom: 1rem;
	}
	.crumbs {
		font-size: 1.15rem;
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
		align-items: baseline;
	}
	.crumbs a {
		color: var(--text-strong);
	}
	.actions {
		display: flex;
		gap: 0.5rem;
	}
	.actions a:hover {
		text-decoration: none;
	}
	.teams {
		margin-bottom: 1rem;
	}
	.teams h2 {
		font-size: 1rem;
		margin: 0 0 0.75rem;
	}
	.team-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1.5rem;
	}
	.team-list a {
		color: var(--text-strong);
		margin-right: 0.4rem;
	}
	.drop {
		min-height: 200px;
		transition: border-color 0.15s;
	}
	.drop--active {
		border-color: var(--accent);
		border-style: dashed;
	}
	.empty {
		text-align: center;
		padding: 3rem 1rem;
	}
	.name a {
		color: var(--text-strong);
		overflow-wrap: anywhere;
		margin-right: 0.5rem;
	}
	.num {
		text-align: right;
		white-space: nowrap;
	}
	.row-actions {
		white-space: nowrap;
		text-align: right;
	}
	.link {
		font: inherit;
		font-size: 0.85rem;
		background: none;
		border: 0;
		padding: 0 0 0 0.75rem;
		color: var(--muted);
		cursor: pointer;
	}
	.link:hover {
		color: var(--text-strong);
		text-decoration: underline;
	}
	.add {
		margin-bottom: 1rem;
	}
	.upload {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 0.5rem 0.75rem;
		margin-bottom: 0.5rem;
		background: var(--surface-2);
		border-radius: 8px;
		font-size: 0.9rem;
	}
	.upload span:first-child {
		flex: 1;
		overflow-wrap: anywhere;
	}
	.upload--error {
		background: var(--error-bg);
		color: var(--error-text);
	}
	.usage {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-top: 1rem;
	}
</style>
