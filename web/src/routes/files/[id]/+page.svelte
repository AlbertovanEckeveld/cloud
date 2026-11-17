<script lang="ts">
	import { page } from '$app/state';
	import FileBrowser from '#lib/components/drive/FileBrowser.svelte';
</script>

<svelte:head>
	<title>Bestanden · Cloud</title>
</svelte:head>

<!-- Re-create the browser when navigating to another folder. -->
{#key page.params.id}
	<FileBrowser folderId={page.params.id ?? null} />
{/key}
