<script lang="ts">
	// A file's history in a side panel: view, download, restore, name or
	// delete each version, and clean up old versions in one go. Named
	// versions survive a clean-up. Actions depend on the permission; the
	// server checks it again.
	import Icon from '#lib/components/Icon.svelte';
	import { api, type DriveNode, type FileVersion } from '#lib/api.ts';
	import { allows, dateTime, formatBytes, previewKind, timeAgo, viaLabels } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';
	import PreviewDialog from './PreviewDialog.svelte';

	let { node, onChanged, onClose }: { node: DriveNode; onChanged: () => void; onClose: () => void } = $props();

	let dialog: HTMLDialogElement;
	let versions = $state<FileVersion[] | null>(null);
	let error = $state<unknown>(null);
	let notice = $state('');
	let naming = $state<{ id: string; label: string } | null>(null);
	let viewing = $state<FileVersion | null>(null);

	const total = $derived(versions?.reduce((sum, v) => sum + v.size, 0) ?? 0);
	// What a clean-up would remove: old versions without a name.
	const prunable = $derived(versions?.filter((v) => !v.current && !v.label) ?? []);
	const prunableBytes = $derived(prunable.reduce((sum, v) => sum + v.size, 0));
	const canEdit = $derived(allows(node.permission, 'edit'));
	const canManage = $derived(allows(node.permission, 'manage'));

	async function load() {
		try {
			versions = await api.drive.versions(node.id);
		} catch (e) {
			error = e;
		}
	}

	$effect(() => {
		dialog.showModal();
		load();
	});

	// Runs an action, reloads, and shows what happened (done gets the
	// action's result).
	async function act<T>(action: () => Promise<T>, done: (result: T) => string = () => '') {
		error = null;
		notice = '';
		try {
			const result = await action();
			await load();
			onChanged();
			notice = done(result);
		} catch (e) {
			error = e;
		}
	}

	const restore = (v: FileVersion) =>
		act(
			() => api.drive.restoreVersion(node.id, v.id),
			() => `De versie van ${dateTime(v.created_at)} is weer de huidige. De andere versies blijven bewaard.`
		);

	const remove = (v: FileVersion) =>
		confirm(`De versie van ${dateTime(v.created_at)} definitief verwijderen? Dit maakt ${formatBytes(v.size)} vrij.`) &&
		act(() => api.drive.deleteVersion(node.id, v.id), () => 'Versie verwijderd.');

	function prune() {
		const n = prunable.length;
		if (!confirm(`${n} oude ${n === 1 ? 'versie' : 'versies'} zonder naam definitief verwijderen? Dit maakt ${formatBytes(prunableBytes)} vrij. De huidige versie en versies met een naam blijven.`)) return;
		act(
			() => api.drive.pruneVersions(node.id),
			(freed) => `${freed.versions} ${freed.versions === 1 ? 'versie' : 'versies'} verwijderd, ${formatBytes(freed.bytes)} vrijgemaakt.`
		);
	}

	function saveName(e: SubmitEvent) {
		e.preventDefault();
		const n = naming;
		if (!n) return;
		act(async () => {
			await api.drive.labelVersion(node.id, n.id, n.label.trim() || null);
			naming = null;
		});
	}

	const preview = (v: FileVersion) => !!previewKind(v.content_type, node.name);
</script>

<dialog bind:this={dialog} class="sheet" onclose={onClose} aria-labelledby="versions-title">
	<header class="sheet__head">
		<div>
			<h2 id="versions-title">Versies</h2>
			<p class="muted small name">{node.name}</p>
		</div>
		<button class="icon-button" aria-label="Sluiten" title="Sluiten (Esc)" onclick={() => dialog.close()}><Icon name="close" /></button>
	</header>

	<ErrorNotice {error} />
	{#if notice}<p class="flash flash--success" role="status">{notice}</p>{/if}

	{#if versions}
		<div class="summary small">
			<span class="muted">
				{versions.length} {versions.length === 1 ? 'versie' : 'versies'} · samen {formatBytes(total)}
			</span>
			{#if canManage && prunable.length > 0}
				<button class="button" onclick={prune}>Oude versies opruimen ({formatBytes(prunableBytes)})</button>
			{/if}
		</div>
		<p class="muted small">
			Elke nieuwe upload of bewerking bewaart de vorige versie. Geef een versie een naam om hem te bewaren bij het opruimen.
		</p>

		<ol class="versions">
			{#each versions as v (v.id)}
				<li class:current={v.current}>
					<div class="when">
						<time datetime={v.created_at} title={timeAgo(v.created_at)}>{dateTime(v.created_at)}</time>
						{#if v.current}<span class="badge badge--ok">Huidige versie</span>{/if}
						{#if v.label}<span class="badge with-icon"><Icon name="tag" size={14} />{v.label}</span>{/if}
					</div>
					<div class="muted small">
						{[v.created_by_name, v.created_via ? viaLabels[v.created_via] : '', formatBytes(v.size)].filter(Boolean).join(' · ')}
					</div>

					{#if naming?.id === v.id}
						<form class="name-form" onsubmit={saveName}>
							<!-- svelte-ignore a11y_autofocus -->
							<input bind:value={naming.label} maxlength="80" placeholder="Bijvoorbeeld: ingeleverd" aria-label="Naam van deze versie" autofocus />
							<button class="button button--primary">Opslaan</button>
							<button type="button" class="button" onclick={() => (naming = null)}>Annuleren</button>
						</form>
					{:else}
						<div class="actions">
							{#if preview(v)}
								<button class="icon-button" aria-label="Bekijken" title="Bekijken" onclick={() => (viewing = v)}><Icon name="eye" /></button>
							{/if}
							<a class="icon-button" href={api.drive.versionURL(node.id, v.id)} download aria-label="Downloaden" title="Downloaden"><Icon name="download" /></a>
							{#if canEdit}
								<button class="icon-button" aria-label={v.label ? 'Naam wijzigen' : 'Naam geven'} title={v.label ? 'Naam wijzigen' : 'Naam geven'}
									onclick={() => (naming = { id: v.id, label: v.label ?? '' })}><Icon name="tag" /></button>
							{/if}
							{#if canEdit && !v.current}
								<button class="icon-button" aria-label="Terugzetten" title="Deze versie weer de huidige maken" onclick={() => restore(v)}><Icon name="restore" /></button>
							{/if}
							{#if canManage && !v.current}
								<button class="icon-button" aria-label="Verwijderen" title="Verwijderen" onclick={() => remove(v)}><Icon name="trash" /></button>
							{/if}
						</div>
					{/if}
				</li>
			{/each}
		</ol>
	{:else if !error}
		<p class="muted">Laden…</p>
	{/if}
</dialog>

{#if viewing}
	<PreviewDialog
		files={[{ ...node, content_type: viewing.content_type, size: viewing.size }]}
		start={0}
		version={{ id: viewing.id, title: `versie van ${dateTime(viewing.created_at)}` }}
		onClose={() => (viewing = null)}
	/>
{/if}

<style>
	/* A panel on the right, the full height of the window. */
	.sheet {
		margin: 0 0 0 auto;
		width: min(480px, 100vw);
		height: 100dvh;
		max-height: none;
		border-radius: 0;
		border-width: 0 0 0 1px;
		overflow-y: auto;
	}
	.sheet__head {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
	}
	.sheet__head h2 {
		margin: 0;
	}
	.name {
		margin: 0.25rem 0 0;
		overflow-wrap: anywhere;
	}
	.summary {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-top: 1rem;
	}
	.versions {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.versions li {
		padding: 0.75rem 0;
		border-top: 1px solid var(--border);
	}
	.versions li.current {
		background: linear-gradient(90deg, var(--surface-2), transparent);
		padding-inline: 0.5rem;
		border-radius: 8px;
	}
	.when {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.actions {
		display: flex;
		gap: 0.15rem;
		margin-top: 0.35rem;
	}
	.name-form {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.5rem;
	}
	.name-form input {
		flex: 1 1 12rem;
	}
</style>
