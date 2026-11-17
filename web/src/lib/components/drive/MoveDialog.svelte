<script lang="ts">
	// Lets the user pick a destination folder by browsing the folder tree.
	// Inside a share, browsing starts at the shared folder: files can only move
	// within their owner's folders.
	import Icon from '#lib/components/Icon.svelte';
	import { api, type DriveNode, type FolderView } from '#lib/api.ts';
	import { allows } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let {
		node,
		root,
		onMove,
		onClose
	}: {
		node: DriveNode;
		/** The shared folder to start at; null for the user's own files. */
		root: { id: string; name: string } | null;
		onMove: (target: string | null) => void;
		onClose: () => void;
	} = $props();

	let dialog: HTMLDialogElement;
	let current = $state<string | null>(null);
	let view = $state<FolderView | null>(null);
	let error = $state<unknown>(null);

	async function open(id: string | null) {
		try {
			view = await api.drive.folder(id);
			current = id;
		} catch (e) {
			error = e;
		}
	}

	$effect(() => {
		dialog.showModal();
		open(root?.id ?? null);
	});

	// The node itself cannot be a destination, nor can its current folder.
	const folders = $derived(view?.children.filter((c) => c.kind === 'folder' && c.id !== node.id) ?? []);
	const allowed = $derived(current !== node.parent_id && (!view?.folder || allows(view.folder.permission, 'edit')));
</script>

<dialog bind:this={dialog} onclose={onClose} aria-labelledby="move-title">
	<h2 id="move-title">"{node.name}" verplaatsen</h2>
	<ErrorNotice {error} />
	<nav class="crumbs" aria-label="Map">
		{#if root}
			{#each view?.path ?? [] as c, i (c.id)}
				{#if i > 0}/{/if} <button class="link-button" onclick={() => open(c.id)}>{c.name}</button>
			{/each}
		{:else}
			<button class="link-button" onclick={() => open(null)}>Bestanden</button>
			{#each view?.path ?? [] as c (c.id)}
				/ <button class="link-button" onclick={() => open(c.id)}>{c.name}</button>
			{/each}
		{/if}
	</nav>
	<ul class="list folders">
		{#each folders as f (f.id)}
			<li><button class="link-button" onclick={() => open(f.id)}><span class="with-icon"><Icon name="folder" />{f.name}</span></button></li>
		{:else}
			<li class="muted">Geen mappen hier.</li>
		{/each}
	</ul>
	<div class="dialog-actions">
		<button class="button button--primary" disabled={!allowed} onclick={() => onMove(current)}>Hierheen verplaatsen</button>
		<button class="button" onclick={() => dialog.close()}>Annuleren</button>
	</div>
</dialog>

<style>
	.crumbs {
		margin-bottom: 0.5rem;
		color: var(--muted);
	}
	.folders {
		max-height: 300px;
		overflow-y: auto;
	}
</style>
