<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { safeNext } from '#lib/format.ts';
	import LoginForm from '#lib/components/LoginForm.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const done = () => goto(safeNext(page.url.searchParams.get('next'), page.url.origin), { invalidateAll: true });
</script>

<svelte:head>
	<title>Inloggen · Cloud</title>
</svelte:head>

<div class="panel auth-card">
	<h1>Inloggen</h1>
	<!-- A page reload between the two steps resumes at the code. -->
	<LoginForm resume={data.session?.state === 'mfa_required'} onDone={done} />
</div>
