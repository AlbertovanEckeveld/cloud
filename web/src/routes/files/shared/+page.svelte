<script lang="ts">
	import Icon from '#lib/components/Icon.svelte';
	import { onMount } from 'svelte';
	import { api, type SharedNode } from '#lib/api.ts';
	import { dateTime, formatBytes, permissionLabel, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import { drive } from '#lib/drive-state.svelte.ts';

	drive.section = 'shared';

	let { data } = $props();

	let nodes = $state<SharedNode[] | null>(null);
	let error = $state<unknown>(null);

	async function load() {
		try {
			nodes = await api.drive.sharedWithMe();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function leave(n: SharedNode) {
		if (!data.user || !confirm(`"${n.name}" niet meer met je delen? Alleen ${n.owner_name || 'de eigenaar'} kan het opnieuw delen.`)) return;
		error = null;
		try {
			await api.drive.unshare(n.id, { type: 'user', id: data.user.id });
			await load();
		} catch (e) {
			error = e;
		}
	}
</script>

<svelte:head>
	<title>Gedeeld met mij · Cloud</title>
</svelte:head>

<h1>Gedeeld met mij</h1>
<p class="muted">Bestanden en mappen die anderen met jou of met een van je groepen hebben gedeeld.</p>
<ErrorNotice {error} />

{#if nodes && nodes.length > 0}
	<div class="panel table-wrap">
		<table class="table">
			<thead><tr><th>Naam</th><th>Van</th><th>Recht</th><th>Gewijzigd</th><th><span class="visually-hidden">Acties</span></th></tr></thead>
			<tbody>
				{#each nodes as n (n.id)}
					<tr>
						<td class="name">
							{#if n.kind === 'folder'}
								<a class="with-icon" href="/files/{n.id}"><Icon name="folder" />{n.name}</a>
							{:else}
								<a class="with-icon" href={api.drive.downloadURL(n.id)} download><Icon name="file" />{n.name}</a>
								<span class="muted small">{formatBytes(n.size)}</span>
							{/if}
						</td>
						<td>{n.owner_name}</td>
						<td><span class="badge">{permissionLabel(n.permission)}</span></td>
						<td class="muted"><time datetime={n.updated_at} title={dateTime(n.updated_at)}>{timeAgo(n.updated_at)}</time></td>
						<td class="row-actions"><button class="button" onclick={() => leave(n)}>Verlaten</button></td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{:else if nodes}
	<p class="muted">Er is nog niets met je gedeeld.</p>
{/if}

<style>
	.name a {
		color: var(--text-strong);
		overflow-wrap: anywhere;
	}
	.row-actions {
		text-align: right;
	}
</style>
