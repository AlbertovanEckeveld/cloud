<script lang="ts">
	import { onMount } from 'svelte';
	import { goto, invalidateAll } from '$app/navigation';
	import { api, type AppGrant } from '#lib/api.ts';
	import { dateTime, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let apps = $state<AppGrant[] | null>(null);
	let error = $state<unknown>(null);

	async function load() {
		try {
			apps = await api.appGrants();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function revoke(a: AppGrant) {
		if (!confirm(`Toegang van ${a.name} intrekken? Je moet daar dan opnieuw inloggen.`)) return;
		error = null;
		try {
			await api.revokeApp(a.client_id);
			await load();
		} catch (e) {
			error = e;
		}
	}

	async function everywhere() {
		if (!confirm('Uitloggen bij Cloud en bij alle apps?')) return;
		try {
			await api.logoutEverywhere();
			await invalidateAll();
			await goto('/login');
		} catch (e) {
			error = e;
		}
	}
</script>

<section class="panel" aria-labelledby="apps-title">
	<h2 id="apps-title">Apps</h2>
	<p class="muted small">Apps waarbij je met je Cloud-account bent ingelogd.</p>
	<ErrorNotice {error} />
	{#if apps && apps.length > 0}
		<ul class="list">
			{#each apps as a (a.client_id)}
				<li>
					<div>
						<span class="list__title">{a.name}</span>
						<span class="muted small block">
							laatst ingelogd <time datetime={a.last_login_at} title={dateTime(a.last_login_at)}>{timeAgo(a.last_login_at)}</time>
						</span>
					</div>
					<button class="button" onclick={() => revoke(a)}>Toegang intrekken</button>
				</li>
			{/each}
		</ul>
	{:else if apps}
		<p class="muted">Je bent nog bij geen enkele app ingelogd.</p>
	{/if}
	<button class="button" onclick={everywhere}>Overal uitloggen, ook bij apps</button>
</section>
