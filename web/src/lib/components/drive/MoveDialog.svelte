<script lang="ts">
	// Lets the user pick a destination folder by browsing the folder tree.
	import { api, type DriveNode, type FolderView } from '#lib/api.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let { node, onMove, onClose }: { node: DriveNode; onMove: (target: string | null) => void; onClose: () => void } =
		$props();

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
		open(null);
	});

	// The node itself cannot be a destination, nor can its current folder.
	const folders = $derived(view?.children.filter((c) => c.kind === 'folder' && c.id !== node.id) ?? []);
	const here = $derived(current === node.parent_id);
</script>

<dialog bind:this={dialog} onclose={onClose} aria-labelledby="move-title">
	<h2 id="move-title">"{node.name}" verplaatsen</h2>
	<ErrorNotice {error} />
	<nav class="crumbs" aria-label="Map">
		<button class="link" onclick={() => open(null)}>Bestanden</button>
		{#each view?.path ?? [] as c (c.id)}
			/ <button class="link" onclick={() => open(c.id)}>{c.name}</button>
		{/each}
	</nav>
	<ul class="list folders">
		{#each folders as f (f.id)}
			<li><button class="link" onclick={() => open(f.id)}>📁 {f.name}</button></li>
		{:else}
			<li class="muted">Geen mappen hier.</li>
		{/each}
	</ul>
	<div class="actions">
		<button class="button button--primary" disabled={here} onclick={() => onMove(current)}>Hierheen verplaatsen</button>
		<button class="button" onclick={() => dialog.close()}>Annuleren</button>
	</div>
</dialog>

<style>
	dialog {
		width: min(480px, 92vw);
		background: var(--surface);
		color: var(--text);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 1.25rem;
	}
	dialog::backdrop {
		background: rgb(0 0 0 / 0.5);
	}
	.crumbs {
		margin-bottom: 0.5rem;
		color: var(--muted);
	}
	.folders {
		max-height: 300px;
		overflow-y: auto;
	}
	.link {
		font: inherit;
		background: none;
		border: 0;
		padding: 0;
		color: var(--accent);
		cursor: pointer;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
	}
</style>
