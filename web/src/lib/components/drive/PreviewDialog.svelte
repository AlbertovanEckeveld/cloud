<script lang="ts">
	// Shows a file in the page: images, video, audio and text. Arrow keys or
	// the buttons go to the previous or next previewable file in the folder.
	// With version, it shows that old version of the one file instead.
	// While the assistant panel is open the viewer is not modal and makes room
	// for it, so you can read and ask at the same time (a modal dialog would
	// make the panel unusable).
	import Icon from '#lib/components/Icon.svelte';
	import { api, errorMessage, type DriveNode } from '#lib/api.ts';
	import { formatBytes, previewKind } from '#lib/format.ts';
	import Markdown from './Markdown.svelte';
	import { askAssistant, assistant } from '#lib/assistant-state.svelte.ts';
	import PdfView from './PdfView.svelte';

	let {
		files,
		start,
		version = null,
		onClose
	}: { files: DriveNode[]; start: number; version?: { id: string; title: string } | null; onClose: () => void } = $props();

	const src = (id: string) => (version ? `${api.drive.versionURL(id, version.id)}?inline=1` : api.drive.previewURL(id));
	const download = (id: string) => (version ? api.drive.versionURL(id, version.id) : api.drive.downloadURL(id));

	// Larger text files are not loaded into the page.
	const maxText = 1 << 20;

	let dialog: HTMLDialogElement;
	// The dialog opens at start and then navigates on its own.
	// svelte-ignore state_referenced_locally
	let index = $state(start);
	let text = $state<string | null>(null);
	let textError = $state<string | null>(null);

	const file = $derived(files[index]);
	const kind = $derived(previewKind(file.content_type, file.name));

	const beside = $derived(assistant.open);

	// Reopening in the other mode closes the dialog first; its close event
	// comes later and is only a real close when the dialog stayed closed.
	$effect(() => {
		const modal = !beside;
		if (dialog.open) dialog.close();
		if (modal) dialog.showModal();
		else dialog.show();
	});

	function closed() {
		if (!dialog.open) onClose();
	}

	$effect(() => {
		text = textError = null;
		if (kind !== 'text' && kind !== 'markdown') return;
		if (file.size > maxText) {
			textError = `Dit bestand is te groot voor een voorbeeld (${formatBytes(file.size)}).`;
			return;
		}
		const id = file.id;
		fetch(src(id))
			.then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
			.then((t) => files[index]?.id === id && (text = t))
			.catch((e) => (textError = errorMessage(e)));
	});

	const go = (step: number) => (index = (index + step + files.length) % files.length);

	function keys(e: KeyboardEvent) {
		// A modal dialog closes on Escape by itself; beside the panel it does not.
		if (e.key === 'Escape' && beside) dialog.close();
		if (files.length < 2) return;
		if (e.key === 'ArrowRight') go(1);
		if (e.key === 'ArrowLeft') go(-1);
	}
</script>

<dialog bind:this={dialog} class="preview" class:beside onclose={closed} onkeydown={keys} aria-labelledby="preview-title">
	<header class="preview__bar">
		<h2 id="preview-title" class="with-icon"><Icon name="file" />{file.name}</h2>
		<span class="muted small">{version ? `${version.title} · ` : ''}{formatBytes(file.size)}</span>
		<span class="preview__actions">
			{#if files.length > 1}
				<button class="icon-button" aria-label="Vorige" title="Vorige (←)" onclick={() => go(-1)}><Icon name="left" /></button>
				<span class="muted small">{index + 1} / {files.length}</span>
				<button class="icon-button" aria-label="Volgende" title="Volgende (→)" onclick={() => go(1)}><Icon name="right" /></button>
			{/if}
			{#if assistant.available && !version}
				<button class="icon-button" aria-label="Samenvatten met de assistent" title="Samenvatten"
					onclick={() => askAssistant('Vat dit bestand samen.', [{ source: 'drive', id: file.id, title: file.name, kind: 'file' }])}><Icon name="spark" /></button>
			{/if}
			<a class="icon-button" href={download(file.id)} download aria-label="Downloaden" title="Downloaden"><Icon name="download" /></a>
			<button class="icon-button" aria-label="Sluiten" title="Sluiten (Esc)" onclick={() => dialog.close()}><Icon name="close" /></button>
		</span>
	</header>
	<div class="preview__body">
		{#key file.id}
			{#if kind === 'image'}
				<img src={src(file.id)} alt={file.name} />
			{:else if kind === 'video'}
				<!-- svelte-ignore a11y_media_has_caption -->
				<video src={src(file.id)} controls preload="metadata"></video>
			{:else if kind === 'audio'}
				<audio src={src(file.id)} controls preload="metadata"></audio>
			{:else if kind === 'pdf'}
				<PdfView url={src(file.id)} />
			{:else if textError}
				<p class="muted">{textError}</p>
			{:else if text !== null && kind === 'markdown'}
				<Markdown source={text} />
			{:else if text !== null}
				<pre>{text}</pre>
			{:else}
				<p class="muted">Laden…</p>
			{/if}
		{/key}
	</div>
</dialog>

<style>
	.preview {
		width: min(1100px, 96vw);
		height: min(820px, 92vh);
		padding: 0;
		display: flex;
		flex-direction: column;
	}
	.preview.beside {
		position: fixed;
		inset: 0 auto 0 0;
		z-index: 39; /* just below the assistant panel */
		margin: 0;
		width: calc(100vw - var(--assistant-width));
		height: 100vh;
		max-height: none;
		border-radius: 0;
	}
	@media (max-width: 760px) {
		/* No room beside the panel: the viewer waits underneath it. */
		.preview.beside {
			width: 100vw;
		}
	}
	.preview:not([open]) {
		display: none;
	}
	.preview__bar {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.6rem 0.9rem;
		border-bottom: 1px solid var(--border);
	}
	.preview__bar h2 {
		font-size: 1rem;
		margin: 0;
		overflow-wrap: anywhere;
	}
	.preview__actions {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 0.25rem;
	}
	.preview__body {
		flex: 1;
		min-height: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		overflow: auto;
		background: var(--bg, #000);
	}
	img,
	video {
		max-width: 100%;
		max-height: 100%;
		object-fit: contain;
	}
	pre {
		align-self: stretch;
		width: 100%;
		margin: 0;
		padding: 1rem;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		font-size: 0.85rem;
	}
</style>
