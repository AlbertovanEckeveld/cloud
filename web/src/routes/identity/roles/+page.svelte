<script lang="ts">
	// Roles and what they may do (ADR 0022): an overview as a matrix of
	// permissions × roles, and per role who has it and a form to change it.
	// Nobody can put a permission in a role that they do not have; the
	// server checks that again, as it checks everything.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import Icon from '#lib/components/Icon.svelte';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';
	import { api, type PermissionInfo, type Role } from '#lib/api.ts';
	import { isReauthError, reauthenticate } from '#lib/auth.ts';
	import { can } from '#lib/permissions.ts';

	type Draft = { id: string | null; name: string; description: string; permissions: Set<string> };

	let roles = $state<Role[] | null>(null);
	let catalog = $state<PermissionInfo[]>([]);
	let error = $state<unknown>(null);
	let draft = $state<Draft | null>(null);
	let busy = $state(false);

	const me = $derived(page.data.user);
	const mayManage = $derived(can(me, 'identity.roles.manage'));
	const mayAssign = $derived(can(me, 'identity.roles.assign'));
	const modules = { identity: 'Identity', drive: 'Drive', system: 'Systeem', assistant: 'Assistent' } as const;
	const byModule = $derived(
		(Object.keys(modules) as (keyof typeof modules)[]).map((m) => ({ module: m, items: catalog.filter((p) => p.module === m) }))
	);
	// The rule of ADR 0022: you can only give what you have yourself.
	const mine = (permission: string) => can(me, permission);
	const coversAll = (r: Role) => r.permissions.every(mine);

	async function load() {
		try {
			[roles, catalog] = await Promise.all([api.admin.roles(), api.admin.permissions()]);
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function act(action: () => Promise<unknown>) {
		busy = true;
		error = null;
		try {
			await action();
			await load();
		} catch (e) {
			if (isReauthError(e)) return reauthenticate(page.url.pathname);
			error = e;
		} finally {
			busy = false;
		}
	}

	const edit = (r: Role) => (draft = { id: r.id, name: r.name, description: r.description, permissions: new Set(r.permissions) });
	const create = () => (draft = { id: null, name: '', description: '', permissions: new Set() });

	function toggle(permission: string, on: boolean) {
		if (!draft) return;
		const next = new Set(draft.permissions);
		if (on) next.add(permission);
		else next.delete(permission);
		draft.permissions = next;
	}

	function save(e: SubmitEvent) {
		e.preventDefault();
		const d = draft;
		if (!d) return;
		const body = { name: d.name, description: d.description, permissions: [...d.permissions] };
		return act(async () => {
			if (d.id) await api.admin.updateRole(d.id, body);
			else await api.admin.createRole(body);
			draft = null;
		});
	}

	const remove = (r: Role) =>
		confirm(`De rol "${r.name}" verwijderen? Wie hem heeft, verliest deze rechten direct.`) && act(() => api.admin.deleteRole(r.id));

	const takeFrom = (r: Role, to: 'users' | 'groups', id: string, name: string) =>
		confirm(`De rol "${r.name}" intrekken bij ${name}?`) && act(() => api.admin.setRole(r.id, to, id, false));
</script>

<svelte:head>
	<title>Rollen · Identity · Cloud</title>
</svelte:head>

<div class="head">
	<p class="muted">
		Een rol bundelt rechten. Je geeft een rol aan een gebruiker of aan een groep (op hun pagina); via een groep hebben alle leden
		hem. Je kunt nooit meer rechten uitdelen dan je zelf hebt.
	</p>
	{#if mayManage}<button class="button button--primary" onclick={create}><span class="with-icon"><Icon name="plus" />Nieuwe rol</span></button>{/if}
</div>

<ErrorNotice {error} />

{#if roles && catalog.length > 0}
	<section class="panel table-wrap" aria-label="Rechten per rol">
		<table class="table matrix">
			<thead>
				<tr>
					<th>Recht</th>
					{#each roles as r (r.id)}<th class="role">{r.name}</th>{/each}
				</tr>
			</thead>
			<tbody>
				{#each byModule as group (group.module)}
					<tr class="module"><th colspan={roles.length + 1}>{modules[group.module]}</th></tr>
					{#each group.items as p (p.name)}
						<tr>
							<td>{p.label}<span class="muted small block"><code>{p.name}</code></span></td>
							{#each roles as r (r.id)}
								<td class="cell">
									{#if r.permissions.includes(p.name)}<span class="yes" title="{r.name} heeft dit recht"><Icon name="check" size={18} /><span class="visually-hidden">ja</span></span>{/if}
								</td>
							{/each}
						</tr>
					{/each}
				{/each}
			</tbody>
		</table>
	</section>

	{#if draft}
		<form class="panel form editor" onsubmit={save}>
			<h2>{draft.id ? `Rol "${draft.name}" wijzigen` : 'Nieuwe rol'}</h2>
			<label class="field">
				<span>Naam</span>
				<input bind:value={draft.name} required maxlength="60" />
			</label>
			<label class="field">
				<span>Omschrijving (optioneel)</span>
				<input bind:value={draft.description} maxlength="300" />
			</label>
			{#each byModule as group (group.module)}
				<fieldset>
					<legend>{modules[group.module]}</legend>
					{#each group.items as p (p.name)}
						<label class="check" title={mine(p.name) ? '' : 'Je hebt dit recht zelf niet'}>
							<input type="checkbox" checked={draft.permissions.has(p.name)} disabled={!mine(p.name)}
								onchange={(e) => toggle(p.name, e.currentTarget.checked)} />
							<span>{p.label}</span>
						</label>
					{/each}
				</fieldset>
			{/each}
			<div class="actions">
				<button class="button button--primary" disabled={busy}>Opslaan</button>
				<button type="button" class="button" onclick={() => (draft = null)}>Annuleren</button>
			</div>
		</form>
	{/if}

	<div class="cards">
		{#each roles as r (r.id)}
			<section class="panel" aria-labelledby="role-{r.id}">
				<div class="card-head">
					<h2 id="role-{r.id}">{r.name}</h2>
					{#if r.builtin}<span class="badge">Ingebouwd</span>{/if}
				</div>
				<p class="muted small">{r.builtin ? 'Alle rechten, ook rechten die later bijkomen. Niet te wijzigen.' : r.description || `${r.permissions.length} rechten`}</p>
				<h3 class="small">Wie heeft deze rol</h3>
				{#if r.users.length === 0 && r.groups.length === 0}
					<p class="muted small">Niemand. Geef hem op de pagina van een gebruiker of groep.</p>
				{:else}
					<ul class="holders">
						{#each r.users as u (u.id)}
							<li>
								<a href="/identity/users/{u.id}" class="with-icon"><Icon name="users" size={16} />{u.name}</a>
								{#if mayAssign && coversAll(r)}
									<button class="icon-button" aria-label="Intrekken bij {u.name}" title="Intrekken" onclick={() => takeFrom(r, 'users', u.id, u.name)}><Icon name="close" size={16} /></button>
								{/if}
							</li>
						{/each}
						{#each r.groups as g (g.id)}
							<li>
								<a href="/identity/groups/{g.id}" class="with-icon"><Icon name="users" size={16} />groep {g.name}</a>
								{#if mayAssign && coversAll(r)}
									<button class="icon-button" aria-label="Intrekken bij groep {g.name}" title="Intrekken" onclick={() => takeFrom(r, 'groups', g.id, `groep ${g.name}`)}><Icon name="close" size={16} /></button>
								{/if}
							</li>
						{/each}
					</ul>
				{/if}
				{#if mayManage && !r.builtin && coversAll(r)}
					<div class="actions">
						<button class="button" onclick={() => edit(r)}>Wijzigen</button>
						<button class="button" onclick={() => remove(r)}>Verwijderen</button>
					</div>
				{/if}
			</section>
		{/each}
	</div>
{:else if !error}
	<p class="muted">Laden…</p>
{/if}

<style>
	.head {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
		flex-wrap: wrap;
		margin-bottom: 1rem;
	}
	.head p {
		max-width: 60ch;
		margin: 0;
	}
	.matrix th.role {
		text-align: center;
		white-space: nowrap;
	}
	.matrix .module th {
		color: var(--text-strong);
		font-weight: 600;
		padding-top: 1rem;
	}
	.matrix .cell {
		text-align: center;
		width: 7rem;
	}
	.yes {
		display: inline-flex;
		color: var(--accent);
	}
	.editor {
		margin-top: 1rem;
	}
	fieldset {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.75rem 1rem;
	}
	legend {
		padding: 0 0.4rem;
		color: var(--muted);
	}
	.cards {
		display: grid;
		gap: 1rem;
		grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
		margin-top: 1rem;
	}
	.card-head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.card-head h2 {
		margin: 0;
	}
	.holders {
		list-style: none;
		padding: 0;
		margin: 0 0 0.75rem;
	}
	.holders li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
</style>
