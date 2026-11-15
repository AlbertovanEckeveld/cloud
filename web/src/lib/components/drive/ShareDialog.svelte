<script lang="ts">
	// Shows who a file or folder is shared with, and lets a manager add people
	// or groups, change their permission or stop sharing.
	import Icon from '#lib/components/Icon.svelte';
	import { page } from '$app/state';
	import { api, type DirectoryEntry, type DriveNode, type Permission, type Share, type ShareLink } from '#lib/api.ts';
	import { dateTime, permissionLabel } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let { node, onClose }: { node: DriveNode; onClose: () => void } = $props();

	const levels: Permission[] = ['view', 'edit', 'manage'];

	let dialog: HTMLDialogElement;
	let shares = $state<Share[] | null>(null);
	let query = $state('');
	let results = $state<DirectoryEntry[]>([]);
	let level = $state<Permission>('view');
	let error = $state<unknown>(null);
	let searchTimer: ReturnType<typeof setTimeout> | undefined;
	let links = $state<ShareLink[]>([]);
	let linkExpires = $state('');
	let linkPassword = $state('');
	// The address of a new link; its token is only shown once.
	let newLink = $state<string | null>(null);

	async function createLink(e: SubmitEvent) {
		e.preventDefault();
		error = null;
		try {
			// A date means "until the end of that day" in the user's time zone.
			const expires = linkExpires ? new Date(`${linkExpires}T23:59:59`).toISOString() : null;
			const l = await api.drive.createLink(node.id, { expires_at: expires, password: linkPassword });
			newLink = `${location.origin}/s#${l.token}`;
			linkPassword = linkExpires = '';
			links = await api.drive.links(node.id);
		} catch (err) {
			error = err;
		}
	}

	async function removeLink(l: ShareLink) {
		error = null;
		try {
			await api.drive.deleteLink(l.id);
			links = await api.drive.links(node.id);
		} catch (err) {
			error = err;
		}
	}

	$effect(() => {
		dialog.showModal();
		api.drive.shares(node.id).then((s) => (shares = s), (e) => (error = e));
		api.drive.links(node.id).then((l) => (links = l), (e) => (error = e));
		return () => clearTimeout(searchTimer);
	});

	function search() {
		clearTimeout(searchTimer);
		const q = query.trim();
		if (q.length < 2) {
			results = [];
			return;
		}
		// Wait until the user stops typing instead of querying every key.
		searchTimer = setTimeout(async () => {
			try {
				const found = await api.directory(q);
				// Sharing with yourself is pointless (and refused by the server).
				const me = page.data.user?.id;
				if (query.trim() === q) results = found.filter((e) => !(e.type === 'user' && e.id === me));
			} catch (e) {
				error = e;
			}
		}, 250);
	}

	const sharedWith = (e: DirectoryEntry) => shares?.some((s) => s.subject.type === e.type && s.subject.id === e.id);

	async function act(action: () => Promise<Share[] | void>) {
		error = null;
		try {
			const updated = await action();
			shares = updated ?? (await api.drive.shares(node.id));
		} catch (e) {
			error = e;
		}
	}

	const add = (e: DirectoryEntry) =>
		act(async () => {
			const updated = await api.drive.share(node.id, { type: e.type, id: e.id }, level);
			query = '';
			results = [];
			return updated;
		});
	const change = (s: Share, p: Permission) => act(() => api.drive.share(node.id, s.subject, p));
	const remove = (s: Share) => act(() => api.drive.unshare(node.id, s.subject));
</script>

<dialog bind:this={dialog} onclose={onClose} aria-labelledby="share-title">
	<h2 id="share-title">"{node.name}" delen</h2>
	<p class="muted small">
		{node.kind === 'folder' ? 'Wie je toegang geeft, ziet ook alles in deze map.' : ''}
		Bekijken = openen en downloaden; bewerken = ook wijzigen, uploaden en verwijderen; beheren = ook delen.
	</p>
	<ErrorNotice {error} />

	<div class="add">
		<label class="visually-hidden" for="share-search">Persoon of groep</label>
		<input id="share-search" type="search" bind:value={query} oninput={search} placeholder="Zoek een persoon of groep" autocomplete="off" />
		<label class="visually-hidden" for="share-level">Recht</label>
		<select id="share-level" bind:value={level}>
			{#each levels as l (l)}<option value={l}>{permissionLabel(l)}</option>{/each}
		</select>
	</div>
	{#if results.length > 0}
		<ul class="list results" aria-label="Zoekresultaten">
			{#each results as r (r.type + r.id)}
				<li>
					<span>
						<Icon name={r.type === 'group' ? 'users' : 'share'} size={16} /> {r.name}
						{#if r.detail}<span class="muted small">{r.detail}</span>{/if}
					</span>
					{#if sharedWith(r)}
						<span class="muted small">Heeft al toegang</span>
					{:else}
						<button class="button" onclick={() => add(r)}>Toevoegen</button>
					{/if}
				</li>
			{/each}
		</ul>
	{:else if query.trim().length >= 2}
		<p class="muted small">Niemand gevonden.</p>
	{/if}

	<h3>Toegang</h3>
	{#if shares && shares.length === 0}
		<p class="muted">Nog met niemand gedeeld.</p>
	{:else if shares}
		<ul class="list">
			{#each shares as s (s.subject.type + s.subject.id)}
				<li>
					<span class="with-icon"><Icon name={s.subject.type === 'group' ? 'users' : 'share'} size={16} />{s.name}</span>
					<span class="row">
						<label class="visually-hidden" for="level-{s.subject.id}">Recht van {s.name}</label>
						<select id="level-{s.subject.id}" value={s.permission} onchange={(e) => change(s, e.currentTarget.value as Permission)}>
							{#each levels as l (l)}<option value={l}>{permissionLabel(l)}</option>{/each}
						</select>
						<button class="link-button" onclick={() => remove(s)}>Intrekken</button>
					</span>
				</li>
			{/each}
		</ul>
	{/if}

	<h3>Openbare link</h3>
	<p class="muted small">Iedereen met de link kan {node.kind === 'folder' ? 'de map bekijken en downloaden' : 'het bestand downloaden'}, zonder account.</p>
	{#if newLink}
		<div class="flash flash--success" role="status">
			<p>Kopieer de link nu; hij wordt niet opnieuw getoond.</p>
			<input class="new-link" readonly value={newLink} onfocus={(e) => e.currentTarget.select()} aria-label="Nieuwe link" />
			<button class="button" onclick={() => newLink && navigator.clipboard.writeText(newLink)}>Kopiëren</button>
		</div>
	{/if}
	{#if links.length > 0}
		<ul class="list">
			{#each links as l (l.id)}
				<li>
					<span class="small">
						Gemaakt {dateTime(l.created_at)}{l.expires_at ? ` · verloopt ${dateTime(l.expires_at)}` : ''}
						{#if l.password}<span class="badge">Wachtwoord</span>{/if}
						<span class="muted">{l.downloads}× gedownload</span>
					</span>
					<button class="link-button" onclick={() => removeLink(l)}>Intrekken</button>
				</li>
			{/each}
		</ul>
	{/if}
	<form class="add link-form" onsubmit={createLink}>
		<label class="small">Verloopt <input type="date" bind:value={linkExpires} /></label>
		<input type="password" bind:value={linkPassword} placeholder="Wachtwoord (optioneel)" autocomplete="new-password" aria-label="Wachtwoord voor de link" />
		<button class="button">Link maken</button>
	</form>

	<div class="dialog-actions">
		<button class="button" onclick={() => dialog.close()}>Sluiten</button>
	</div>
</dialog>

<style>
	.add {
		display: flex;
		gap: 0.5rem;
	}
	.add input {
		flex: 1;
	}
	.list li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
	}
	.results {
		max-height: 220px;
		overflow-y: auto;
	}
	h3 {
		margin-top: 1.25rem;
	}
	.new-link {
		width: 100%;
		margin: 0.5rem 0;
	}
	.link-form {
		flex-wrap: wrap;
		align-items: center;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
</style>
