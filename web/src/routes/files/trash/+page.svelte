<script lang="ts">
	// The user's own trash, or with ?team=<id> the trash of a team folder.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, type DriveNode, type TeamRef } from '#lib/api.ts';
	import { allows, dateTime, formatBytes, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	const teamId = page.url.searchParams.get('team');

	let nodes = $state<DriveNode[] | null>(null);
	let team = $state<TeamRef | null>(null);
	let error = $state<unknown>(null);
	let restored = $state<string | null>(null);

	async function load() {
		try {
			if (teamId) {
				({ team, nodes } = await api.drive.teamTrash(teamId));
			} else {
				nodes = await api.drive.listTrash();
			}
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function act(action: () => Promise<unknown>) {
		error = null;
		restored = null;
		try {
			await action();
			await load();
		} catch (e) {
			error = e;
		}
	}

	// In a team folder, deleting for good needs manage; in your own trash you have it.
	const canPurge = $derived(!nodes || nodes.every((n) => allows(n.permission, 'manage')));

	const restore = (n: DriveNode) =>
		act(async () => {
			const back = await api.drive.restore(n.id);
			restored = back.name;
		});
	const remove = (n: DriveNode) =>
		confirm(`"${n.name}" definitief verwijderen? Dit kan niet ongedaan worden gemaakt.`) && act(() => api.drive.deleteForever(n.id));
	const empty = () =>
		confirm('Alles in de prullenbak definitief verwijderen?') &&
		act(() => (teamId ? api.drive.emptyTeamTrash(teamId) : api.drive.emptyTrash()));
</script>

<svelte:head>
	<title>Prullenbak · Cloud</title>
</svelte:head>

<p><a href={team ? `/files/${team.root_id}` : '/files'}>← {team ? team.name : 'Bestanden'}</a></p>
<div class="header">
	<h1>{team ? `Prullenbak van ${team.name}` : 'Prullenbak'}</h1>
	{#if nodes && nodes.length > 0 && canPurge}<button class="button" onclick={empty}>Prullenbak legen</button>{/if}
</div>
<p class="muted">
	Verwijderde bestanden en mappen blijven 30 dagen bewaard en tellen zolang mee voor {team ? 'de opslag van de teammap' : 'je opslag'}.
</p>
<ErrorNotice {error} />
{#if restored}<p class="flash flash--success" role="status">"{restored}" is teruggezet.</p>{/if}

{#if nodes && nodes.length > 0}
	<div class="panel table-wrap">
		<table class="table">
			<thead><tr><th>Naam</th><th>Grootte</th><th>Verwijderd</th><th></th></tr></thead>
			<tbody>
				{#each nodes as n (n.id)}
					<tr>
						<td>{n.kind === 'folder' ? '📁' : '📄'} {n.name}</td>
						<td class="muted">{n.kind === 'file' ? formatBytes(n.size) : ''}</td>
						<td class="muted">
							{#if n.trashed_at}<time datetime={n.trashed_at} title={dateTime(n.trashed_at)}>{timeAgo(n.trashed_at)}</time>{/if}
						</td>
						<td class="row-actions">
							<button class="button" onclick={() => restore(n)}>Terugzetten</button>
							{#if allows(n.permission, 'manage')}
								<button class="button" onclick={() => remove(n)}>Definitief verwijderen</button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{:else if nodes}
	<p class="muted">De prullenbak is leeg.</p>
{/if}

<style>
	.header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
	}
	.row-actions {
		text-align: right;
		white-space: nowrap;
	}
</style>
