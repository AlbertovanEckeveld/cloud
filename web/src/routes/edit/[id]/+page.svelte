<script lang="ts">
	// Edits (or, without edit permission, reads) a document in Collabora Online.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, type OfficeInfo, type OfficeSession } from '#lib/api.ts';
	import Icon from '#lib/components/Icon.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import OfficeFrame from '#lib/components/drive/OfficeFrame.svelte';

	let session = $state<OfficeSession | null>(null);
	let office = $state<OfficeInfo | null>(null);
	let error = $state<unknown>(null);

	const id = page.params.id ?? '';

	function back() {
		if (history.length > 1) history.back();
		else location.assign('/files');
	}

	onMount(async () => {
		try {
			[office, session] = await Promise.all([api.drive.office(), api.drive.openInOffice(id)]);
		} catch (e) {
			error = e;
		}
	});
</script>

<svelte:head>
	<title>Document · Cloud</title>
</svelte:head>

<div class="editor">
	<div class="editor__bar">
		<button class="icon-button" aria-label="Terug" title="Terug" onclick={back}><Icon name="back" /></button>
		{#if session && !session.edit}<span class="badge">Alleen lezen</span>{/if}
		<a class="icon-button" href={api.drive.downloadURL(id)} download aria-label="Downloaden" title="Downloaden"><Icon name="download" /></a>
	</div>
	<ErrorNotice {error} />
	{#if session && office}
		<OfficeFrame {session} origin={office.origin} onClose={back} />
	{/if}
</div>

<style>
	.editor {
		display: flex;
		flex-direction: column;
		height: calc(100vh - 7rem);
	}
	.editor__bar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}
	.editor__bar a:last-child {
		margin-left: auto;
	}
</style>
