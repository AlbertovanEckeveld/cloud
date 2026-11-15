<script lang="ts">
	// A file's history: download any version, make an older one current again,
	// or delete old versions to free storage.
	import { api, type DriveNode, type FileVersion } from '#lib/api.ts';
	import { allows, dateTime, formatBytes, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let { node, onChanged, onClose }: { node: DriveNode; onChanged: () => void; onClose: () => void } = $props();

	let dialog: HTMLDialogElement;
	let versions = $state<FileVersion[] | null>(null);
	let error = $state<unknown>(null);

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

	async function act(action: () => Promise<unknown>) {
		error = null;
		try {
			await action();
			await load();
			onChanged();
		} catch (e) {
			error = e;
		}
	}

	const restore = (v: FileVersion) => act(() => api.drive.restoreVersion(node.id, v.id));
	const remove = (v: FileVersion) =>
		confirm(`De versie van ${dateTime(v.created_at)} definitief verwijderen?`) && act(() => api.drive.deleteVersion(node.id, v.id));
</script>

<dialog bind:this={dialog} onclose={onClose} aria-labelledby="versions-title">
	<h2 id="versions-title">Versies van "{node.name}"</h2>
	<p class="muted small">Elke upload van een bestaand bestand bewaart de vorige versie. Oude versies tellen mee voor de opslag.</p>
	<ErrorNotice {error} />
	{#if versions}
		<ul class="list">
			{#each versions as v (v.id)}
				<li>
					<span>
						<time datetime={v.created_at} title={timeAgo(v.created_at)}>{dateTime(v.created_at)}</time>
						<span class="muted small">{formatBytes(v.size)}{v.created_by_name ? ` · ${v.created_by_name}` : ''}</span>
						{#if v.current}<span class="badge badge--ok">Huidige versie</span>{/if}
					</span>
					<span class="row">
						<a href={api.drive.versionURL(node.id, v.id)} download>Downloaden</a>
						{#if !v.current && allows(node.permission, 'edit')}
							<button class="link-button" onclick={() => restore(v)}>Terugzetten</button>
						{/if}
						{#if !v.current && allows(node.permission, 'manage')}
							<button class="link-button" onclick={() => remove(v)}>Verwijderen</button>
						{/if}
					</span>
				</li>
			{/each}
		</ul>
	{/if}
	<div class="dialog-actions">
		<button class="button" onclick={() => dialog.close()}>Sluiten</button>
	</div>
</dialog>

<style>
	.list li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
	}
	.row {
		display: flex;
		gap: 0.75rem;
	}
</style>
