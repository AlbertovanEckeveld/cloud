<script lang="ts">
	import { onMount } from 'svelte';
	import { goto, invalidateAll } from '$app/navigation';
	import { api, type SessionInfo } from '#lib/api.ts';
	import { dateTime, describeUserAgent, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';
	import Pager from '../Pager.svelte';
	import { pageOf } from '#lib/paging.ts';

	let sessions = $state<SessionInfo[] | null>(null);
	let error = $state<unknown>(null);
	let page = $state(0);
	const shown = $derived(pageOf(sessions ?? [], page, 3));

	async function load() {
		try {
			sessions = await api.sessions();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function revoke(s: SessionInfo) {
		error = null;
		try {
			await api.revokeSession(s.id);
			if (s.current) {
				await invalidateAll();
				await goto('/login');
				return;
			}
			await load();
		} catch (e) {
			error = e;
		}
	}

	async function revokeOthers() {
		if (!confirm('Op alle andere apparaten uitloggen?')) return;
		error = null;
		try {
			await api.revokeOtherSessions();
			await load();
		} catch (e) {
			error = e;
		}
	}
</script>

<section class="panel" aria-labelledby="sessions-title">
	<h2 id="sessions-title">Ingelogde apparaten</h2>
	<ErrorNotice {error} />
	{#if sessions}
		<ul class="list">
			{#each shown.items as s (s.id)}
				<li>
					<div>
						<span class="list__title">{describeUserAgent(s.user_agent)}</span>
						{#if s.current}<span class="badge badge--ok">Dit apparaat</span>{/if}
						<span class="muted small block">
							{s.ip} · actief <time datetime={s.last_seen_at} title={dateTime(s.last_seen_at)}>{timeAgo(s.last_seen_at)}</time>
							· ingelogd <time datetime={s.created_at} title={dateTime(s.created_at)}>{timeAgo(s.created_at)}</time>
						</span>
					</div>
					<button class="button" onclick={() => revoke(s)}>Uitloggen</button>
				</li>
			{/each}
		</ul>
		<Pager label={shown.label} canPrev={shown.page > 0} canNext={shown.page < shown.pages - 1}
			onprev={() => (page = shown.page - 1)} onnext={() => (page = shown.page + 1)} />
		{#if sessions.length > 1}
			<button class="button" onclick={revokeOthers}>Overal anders uitloggen</button>
		{/if}
	{/if}
</section>
