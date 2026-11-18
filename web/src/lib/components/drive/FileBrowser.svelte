<script lang="ts">
	// A folder's contents with everything you can do with them: create
	// folders, upload (button or drag and drop), download, rename, move, move
	// to the trash, share and see versions. Actions depend on the user's
	// permission; the server checks it again for every request.
	import Icon from '#lib/components/Icon.svelte';
	import { onDestroy, onMount } from 'svelte';
	import { page } from '$app/state';
	import { drive, editable, refreshDrive } from '#lib/drive-state.svelte.ts';
	import { api, ApiError, errorMessage, type DriveNode, type FolderView } from '#lib/api.ts';
	import { allows, dateTime, formatBytes, permissionLabel, previewKind, remaining, timeAgo, uniqueName } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';
	import MoveDialog from './MoveDialog.svelte';
	import ShareDialog from './ShareDialog.svelte';
	import VersionsPanel from './VersionsPanel.svelte';
	import PreviewDialog from './PreviewDialog.svelte';
	import { dismissible } from '#lib/dismiss.ts';

	let { folderId }: { folderId: string | null } = $props();

	type UploadState = { key: number; name: string; progress: number; size: number; started: number; error: string | null };

	let view = $state<FolderView | null>(null);
	let fileInput: HTMLInputElement;
	let error = $state<unknown>(null);
	let uploads = $state<UploadState[]>([]);
	let newFolder = $state<string | null>(null);
	let renaming = $state<{ id: string; name: string } | null>(null);
	let moving = $state<DriveNode | null>(null);
	let sharing = $state<DriveNode | null>(null);
	let history = $state<DriveNode | null>(null);
	let previewing = $state<number | null>(null);
	const previewable = $derived(view?.children.filter((c) => c.kind === 'file' && previewKind(c.content_type, c.name)) ?? []);
	let dragging = $state(false);
	let uploadKey = 0;
	// Ticks while uploading, so the time estimates stay current.
	let now = $state(Date.now());
	$effect(() => {
		if (uploads.length === 0) return;
		const timer = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(timer);
	});

	async function load() {
		try {
			view = await api.drive.folder(folderId);
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function act(action: () => Promise<unknown>) {
		error = null;
		try {
			await action();
			await Promise.all([load(), refreshDrive()]);
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
		const state: UploadState = { key: uploadKey++, name: file.name, progress: 0, size: file.size, started: Date.now(), error: null };
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
		await Promise.all([load(), refreshDrive()]);
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

	// Tell the sidebar where we are and what "Nieuw" may do here.
	$effect(() => {
		drive.section = view?.team ? { team: view.team.root_id } : view?.shared ? 'shared' : 'mine';
		drive.create = canEdit ? { folder: () => (newFolder = ''), upload: () => fileInput.click() } : null;
	});
	onDestroy(() => (drive.create = null));
</script>

<!-- What can be done with one item. Wide rows show these as icons; narrow
     ones in a menu with labels (see the container queries below). -->
{#snippet itemActions(n: DriveNode, labelled: boolean)}
	{#if allows(n.permission, 'manage')}
		<button class="icon-button" role={labelled ? 'menuitem' : undefined} aria-label="Delen" title="Delen" onclick={() => (sharing = n)}><Icon name="share" />{#if labelled}<span>Delen</span>{/if}</button>
	{/if}
	{#if n.kind === 'file' && editable(n.name)}
		{@const label = allows(n.permission, 'edit') ? 'Bewerken' : 'Openen'}
		<a class="icon-button" role={labelled ? 'menuitem' : undefined} href="/edit/{n.id}" aria-label={label} title={label}><Icon name="edit" />{#if labelled}<span>{label}</span>{/if}</a>
	{/if}
	{#if n.kind === 'file'}
		<a class="icon-button" role={labelled ? 'menuitem' : undefined} href={api.drive.downloadURL(n.id)} download aria-label="Downloaden" title="Downloaden"><Icon name="download" />{#if labelled}<span>Downloaden</span>{/if}</a>
		<button class="icon-button" role={labelled ? 'menuitem' : undefined} aria-label="Versies" title="Versies" onclick={() => (history = n)}><Icon name="history" />{#if labelled}<span>Versies</span>{/if}</button>
	{/if}
	{#if allows(n.permission, 'edit')}
		<button class="icon-button" role={labelled ? 'menuitem' : undefined} aria-label="Hernoemen" title="Hernoemen" onclick={() => (renaming = { id: n.id, name: n.name })}><Icon name="rename" />{#if labelled}<span>Hernoemen</span>{/if}</button>
		<button class="icon-button" role={labelled ? 'menuitem' : undefined} aria-label="Verplaatsen" title="Verplaatsen" onclick={() => (moving = n)}><Icon name="move" />{#if labelled}<span>Verplaatsen</span>{/if}</button>
		<button class="icon-button" role={labelled ? 'menuitem' : undefined} aria-label="Verwijderen" title="Verwijderen" onclick={() => trash(n)}><Icon name="trash" />{#if labelled}<span>Verwijderen</span>{/if}</button>
	{/if}
{/snippet}

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
		<input type="file" multiple hidden bind:this={fileInput} onchange={pick} />
		{#if view?.team}
			<a class="button" href="/files/trash?team={view.team.id}">Prullenbak van {view.team.name}</a>
		{/if}
	</div>
</div>

<ErrorNotice {error} />


{#each uploads as u (u.key)}
	<div class="upload" class:upload--error={u.error}>
		<span>{u.name}</span>
		{#if u.error}
			<span>{u.error}</span>
			<button class="link" onclick={() => (uploads = uploads.filter((x) => x.key !== u.key))}>Sluiten</button>
		{:else}
			<span class="muted small upload__eta">
				{Math.round(u.progress * 100)}%{remaining(u.progress * u.size, u.size, u.started, now) ? ` · ${remaining(u.progress * u.size, u.size, u.started, now)}` : ''}
			</span>
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
		<p class="empty muted">Deze map is leeg.{canEdit ? ' Sleep bestanden hierheen of gebruik Nieuw.' : ''}</p>
	{:else if view}
		<table class="table files-table">
			<thead><tr><th>Naam</th><th class="num wide-only">Grootte</th><th class="wide-only">Gewijzigd</th><th><span class="visually-hidden">Acties</span></th></tr></thead>
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
								<a class="with-icon" href="/files/{n.id}"><Icon name="folder" />{n.name}</a>
							{:else}
								<!-- Our own preview first (text, markdown, PDF, media); the editor for documents. -->
								{#if editable(n.name) && !previewKind(n.content_type, n.name)}
									<a class="with-icon" href="/edit/{n.id}"><Icon name="file" />{n.name}</a>
								{:else if previewKind(n.content_type, n.name)}
									<button class="link-button with-icon name-button" onclick={() => (previewing = previewable.indexOf(n))}>
										<Icon name="file" />{n.name}
									</button>
								{:else}
									<a class="with-icon" href={api.drive.downloadURL(n.id)} download><Icon name="file" />{n.name}</a>
								{/if}
							{/if}
							{#if n.shared}<span class="badge" title="Gedeeld met anderen"><Icon name="users" size={14} />Gedeeld</span>{/if}
							<!-- Narrow rows: size and date under the name instead of in columns. -->
							<span class="meta narrow-only muted small">
								{n.kind === 'file' ? `${formatBytes(n.size)} · ` : ''}<time datetime={n.updated_at}>{timeAgo(n.updated_at)}</time>{n.updated_by_name ? ` · ${n.updated_by === page.data.user?.id ? 'jij' : n.updated_by_name}` : ''}
							</span>
						</td>
						<td class="num muted wide-only">{n.kind === 'file' ? formatBytes(n.size) : ''}</td>
						<td class="muted wide-only when">
							<time datetime={n.updated_at} title={dateTime(n.updated_at)}>{timeAgo(n.updated_at)}</time>
							{#if n.updated_by_name}
								<span class="editor">· {n.updated_by === page.data.user?.id ? 'jij' : n.updated_by_name}</span>
							{/if}
						</td>
						<td class="row-actions">
							<div class="row-actions__icons">{@render itemActions(n, false)}</div>
							<details class="row-menu" use:dismissible>
								<summary class="icon-button" aria-label="Acties voor {n.name}" title="Acties"><Icon name="more" /></summary>
								<div class="row-menu__list" role="menu">{@render itemActions(n, true)}</div>
							</details>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>


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
{#if previewing !== null && previewing >= 0}
	<PreviewDialog files={previewable} start={previewing} onClose={() => (previewing = null)} />
{/if}
{#if history}
	<VersionsPanel node={history} onChanged={() => Promise.all([load(), refreshDrive()])} onClose={() => (history = null)} />
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
	.drop {
		min-height: 200px;
		transition: border-color 0.15s;
		/* The table adapts to the room it gets, not to the window: the sidebar
		   takes part of the width. */
		container-type: inline-size;
	}
	.files-table .name {
		min-width: 12rem;
	}
	.meta {
		display: block;
		margin-top: 0.15rem;
	}
	.narrow-only,
	.row-menu {
		display: none;
	}
	.row-menu {
		position: relative;
	}
	.row-menu summary {
		list-style: none;
	}
	.row-menu summary::-webkit-details-marker {
		display: none;
	}
	.row-menu__list {
		position: absolute;
		z-index: 10;
		right: 0;
		top: calc(100% + 0.25rem);
		min-width: 190px;
		display: flex;
		flex-direction: column;
		padding: 0.35rem;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		box-shadow: 0 8px 24px rgb(0 0 0 / 0.35);
	}
	.row-menu__list .icon-button {
		width: auto;
		height: auto;
		justify-content: flex-start;
		gap: 0.65rem;
		padding: 0.55rem 0.75rem;
		border-radius: 8px;
		color: var(--text);
		font: inherit;
		text-decoration: none;
	}
	/* Not enough room for all icons: one menu per row. */
	@container (max-width: 760px) {
		.row-actions__icons {
			display: none;
		}
		.row-menu {
			display: inline-block;
		}
	}
	/* Narrower still: size and date move under the name. */
	@container (max-width: 520px) {
		.wide-only {
			display: none;
		}
		.narrow-only {
			display: block;
		}
		.files-table .name {
			min-width: 0;
		}
		.files-table th,
		.files-table td {
			padding-inline: 0.4rem;
		}
	}
	.drop--active {
		border-color: var(--accent);
		border-style: dashed;
	}
	.empty {
		text-align: center;
		padding: 3rem 1rem;
	}
	.name-button {
		color: var(--text-strong);
		text-align: left;
		overflow-wrap: anywhere;
	}
	.name a {
		color: var(--text-strong);
		overflow-wrap: anywhere;
		margin-right: 0.5rem;
	}
	.editor,
	.when {
		white-space: nowrap;
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
	.upload__eta {
		white-space: nowrap;
	}
	.upload--error {
		background: var(--error-bg);
		color: var(--error-text);
	}
</style>
