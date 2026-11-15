<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type AdminUser, type GroupDetail } from '#lib/api.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	let group = $state<GroupDetail | null>(null);
	let users = $state<AdminUser[]>([]);
	let error = $state<unknown>(null);
	let saved = $state(false);
	let busy = $state(false);
	let name = $state('');
	let description = $state('');
	let newMember = $state('');

	const id = $derived(page.params.id ?? '');
	const candidates = $derived(users.filter((u) => !group?.members.some((m) => m.id === u.id)));

	async function load() {
		try {
			[group, users] = await Promise.all([api.admin.group(id), api.admin.users()]);
			name = group.name;
			description = group.description;
		} catch (e) {
			error = e;
		}
	}

	$effect(() => {
		if (id) load();
	});

	async function act(action: () => Promise<void>) {
		busy = true;
		error = null;
		saved = false;
		try {
			await action();
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	function save(e: SubmitEvent) {
		e.preventDefault();
		return act(async () => {
			await api.admin.updateGroup(id, name, description);
			saved = true;
			await load();
		});
	}

	function addMember(e: SubmitEvent) {
		e.preventDefault();
		return act(async () => {
			await api.admin.setMember(id, newMember, true);
			newMember = '';
			await load();
		});
	}

	const removeMember = (userId: string) =>
		act(async () => {
			await api.admin.setMember(id, userId, false);
			await load();
		});

	const remove = () =>
		confirm(`Groep "${group?.name}" verwijderen? De leden blijven bestaan.`) &&
		act(async () => {
			await api.admin.deleteGroup(id);
			await goto('/admin/groups');
		});
</script>

<svelte:head>
	<title>{group?.name ?? 'Groep'} · Beheer · Cloud</title>
</svelte:head>

<p><a href="/admin/groups">← Groepen</a></p>
<ErrorNotice {error} />

{#if group}
	<h1>{group.name}</h1>
	<div class="columns">
		<section class="panel">
			<h2>Leden</h2>
			{#if group.members.length > 0}
				<ul class="list">
					{#each group.members as m (m.id)}
						<li>
							<div>
								<a class="list__title" href="/admin/users/{m.id}">{m.display_name}</a>
								<code>{m.username}</code>
							</div>
							<button class="button" onclick={() => removeMember(m.id)} disabled={busy}>Verwijderen</button>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="muted">Deze groep heeft nog geen leden.</p>
			{/if}
			{#if candidates.length > 0}
				<form class="add" onsubmit={addMember}>
					<select bind:value={newMember} required aria-label="Gebruiker toevoegen">
						<option value="" disabled>Kies een gebruiker…</option>
						{#each candidates as u (u.id)}
							<option value={u.id}>{u.display_name} ({u.username})</option>
						{/each}
					</select>
					<button class="button button--primary" disabled={busy || !newMember}>Toevoegen</button>
				</form>
			{/if}
		</section>

		<div class="stack">
			<form class="panel form" onsubmit={save}>
				<h2>Instellingen</h2>
				{#if saved}<p class="flash flash--success" role="status">Opgeslagen.</p>{/if}
				<label class="field">
					<span>Naam</span>
					<input bind:value={name} required maxlength="32" autocapitalize="none" />
					<p class="field__hint">Let op: apps die rechten aan deze naam koppelen, moeten mee veranderen.</p>
				</label>
				<label class="field">
					<span>Omschrijving</span>
					<input bind:value={description} maxlength="200" />
				</label>
				<div><button class="button button--primary" disabled={busy}>Opslaan</button></div>
			</form>
			<section class="panel">
				<h2>Groep verwijderen</h2>
				<p class="muted small">De leden blijven bestaan; alleen de groep verdwijnt.</p>
				<button class="button" onclick={remove} disabled={busy}>Verwijderen</button>
			</section>
		</div>
	</div>
{/if}
