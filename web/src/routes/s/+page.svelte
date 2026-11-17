<script lang="ts">
	// A public share link: /s#<token>. The token stays in the fragment, which
	// browsers never send to the server, and is removed from the address bar.
	// It goes to the API in request bodies only (ADR 0012).
	import Icon from '#lib/components/Icon.svelte';
	import { onMount, tick } from 'svelte';
	import { replaceState } from '$app/navigation';
	import { api, ApiError, type DriveNode, type OfficeInfo, type OfficeSession, type PublicView } from '#lib/api.ts';
	import OfficeFrame from '#lib/components/drive/OfficeFrame.svelte';
	import { dateTime, formatBytes, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	let token = $state('');
	let password = $state('');
	let needsPassword = $state(false);
	let view = $state<PublicView | null>(null);
	let error = $state<unknown>(null);
	let missing = $state(false);
	let download: HTMLFormElement;
	let fileId = $state('');
	let office = $state<OfficeInfo | null>(null);
	let reading = $state<{ name: string; session: OfficeSession } | null>(null);

	// Documents Collabora can show; through a link always read-only.
	const readable = (n: DriveNode) =>
		!!office?.enabled && office.extensions.includes(n.name.split('.').pop()?.toLowerCase() ?? '');

	async function read(n: DriveNode) {
		error = null;
		try {
			reading = { name: n.name, session: await api.drive.openLinkInOffice(token, password, n.id) };
		} catch (e) {
			error = e;
		}
	}

	async function open(folderId: string | null) {
		error = null;
		try {
			view = await api.drive.openLink(token, password, folderId);
			needsPassword = false;
		} catch (e) {
			if (e instanceof ApiError && e.code === 'password_required') needsPassword = true;
			else if (e instanceof ApiError && e.status === 404) missing = true;
			else error = e;
		}
	}

	onMount(() => {
		token = location.hash.slice(1);
		replaceState(location.pathname, {});
		if (!token) {
			missing = true;
			return;
		}
		open(null);
		api.drive.office().then((o) => (office = o), () => (office = null));
	});

	function unlock(e: SubmitEvent) {
		e.preventDefault();
		open(null);
	}

	// A form post downloads the file natively (also gigabytes) without the
	// token ever appearing in a URL.
	async function get(n: DriveNode) {
		fileId = n.id;
		await tick(); // let the hidden form pick up the file ID
		download.requestSubmit();
	}
</script>

<svelte:head>
	<title>Gedeelde link · Cloud</title>
	<meta name="referrer" content="no-referrer" />
</svelte:head>

<form bind:this={download} method="POST" action={api.drive.linkDownloadURL} hidden>
	<input name="token" value={token} />
	<input name="password" value={password} />
	<input name="file_id" value={fileId} />
</form>

<div class="link-page">
	{#if missing}
		<h1>Link niet gevonden</h1>
		<p class="muted">Deze link bestaat niet (meer), is verlopen of is ingetrokken.</p>
	{:else if needsPassword}
		<form class="panel form" onsubmit={unlock}>
			<h1>Beveiligde link</h1>
			<ErrorNotice {error} />
			<label class="field">
				<span>Wachtwoord</span>
				<!-- svelte-ignore a11y_autofocus -->
				<input type="password" bind:value={password} autocomplete="off" required autofocus />
			</label>
			<div><button class="button button--primary">Openen</button></div>
		</form>
	{:else if reading && office}
		<div class="reader">
			<div class="reader__bar">
				<button class="icon-button" aria-label="Terug" title="Terug" onclick={() => (reading = null)}><Icon name="back" /></button>
				<span>{reading.name}</span>
				<span class="badge">Alleen lezen</span>
			</div>
			<OfficeFrame session={reading.session} origin={office.origin} onClose={() => (reading = null)} />
		</div>
	{:else if view}
		<ErrorNotice {error} />
		{#if view.folder}
			<nav class="crumbs" aria-label="Map">
				{#each view.path as c, i (c.id)}
					{#if i > 0}<span class="muted">/</span>{/if}
					<button class="link-button" onclick={() => open(c.id === view?.node.id ? null : c.id)}>{c.name}</button>
				{/each}
			</nav>
			<section class="panel">
				{#if view.children.length === 0}
					<p class="muted">Deze map is leeg.</p>
				{:else}
					<table class="table">
						<thead><tr><th>Naam</th><th class="num">Grootte</th><th>Gewijzigd</th></tr></thead>
						<tbody>
							{#each view.children as n (n.id)}
								<tr>
									<td>
										{#if n.kind === 'file' && readable(n)}
											<button class="icon-button" aria-label="Openen" title="Openen" onclick={() => read(n)}><Icon name="edit" /></button>
										{/if}
										{#if n.kind === 'folder'}
											<button class="link-button" onclick={() => open(n.id)}><span class="with-icon"><Icon name="folder" />{n.name}</span></button>
										{:else}
											<button class="link-button" onclick={() => get(n)}><span class="with-icon"><Icon name="file" />{n.name}</span></button>
										{/if}
									</td>
									<td class="num muted">{n.kind === 'file' ? formatBytes(n.size) : ''}</td>
									<td class="muted"><time datetime={n.updated_at} title={dateTime(n.updated_at)}>{timeAgo(n.updated_at)}</time></td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			</section>
		{:else}
			<section class="panel file">
				<h1><Icon name="file" size={28} /> {view.node.name}</h1>
				<p class="muted">{formatBytes(view.node.size)} · gewijzigd {timeAgo(view.node.updated_at)}</p>
				{#if readable(view.node)}
					<button class="button with-icon" onclick={() => view && read(view.node)}><Icon name="edit" />Openen</button>
				{/if}
				<button class="button button--primary with-icon" onclick={() => view && get(view.node)}><Icon name="download" />Downloaden</button>
			</section>
		{/if}
	{/if}
</div>

<style>
	.link-page {
		max-width: 1100px;
		margin: 2rem auto;
	}
	.crumbs {
		font-size: 1.15rem;
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
		margin-bottom: 1rem;
	}
	.num {
		text-align: right;
	}
	.file {
		text-align: center;
	}
	.reader {
		display: flex;
		flex-direction: column;
		height: calc(100vh - 8rem);
	}
	.reader__bar {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}
	.file h1 {
		overflow-wrap: anywhere;
	}
</style>
