<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AuditEntry } from '#lib/api.ts';
	import { dateTime, describeUserAgent, eventLabel, isWarningEvent } from '#lib/format.ts';
	import ErrorNotice from '#lib/components/ErrorNotice.svelte';

	const pageSize = 50;

	let entries = $state<AuditEntry[]>([]);
	let more = $state(true);
	let busy = $state(false);
	let error = $state<unknown>(null);

	async function loadMore() {
		busy = true;
		try {
			const page = await api.admin.audit(entries.at(-1)?.id ?? null);
			entries = [...entries, ...page];
			more = page.length === pageSize;
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
	onMount(loadMore);

	const details = (d: Record<string, unknown>) =>
		Object.entries(d)
			.map(([k, v]) => `${k}: ${typeof v === 'string' ? v : JSON.stringify(v)}`)
			.join(', ');
</script>

<svelte:head>
	<title>Audit-log · Identity · Cloud</title>
</svelte:head>

<h1>Audit-log</h1>
<p class="muted">Alle beveiligingsgebeurtenissen van alle gebruikers, nieuwste eerst.</p>
<ErrorNotice {error} />

<div class="panel table-wrap">
	<table class="table">
		<thead>
			<tr><th>Tijd</th><th>Gebeurtenis</th><th>Door</th><th>Betreft</th><th>Details</th><th>Herkomst</th></tr>
		</thead>
		<tbody>
			{#each entries as e (e.id)}
				<tr class:warning={isWarningEvent(e.event)}>
					<td class="nowrap"><time datetime={e.occurred_at}>{dateTime(e.occurred_at)}</time></td>
					<td class="event">{eventLabel(e.event)}</td>
					<td class="nowrap">{e.actor ?? '—'}</td>
					<td class="nowrap">{e.subject ?? '—'}</td>
					<td class="small">{details(e.details)}</td>
					<td class="small muted">
						{e.ip || '—'}{#if e.user_agent}<br />{describeUserAgent(e.user_agent)}{/if}<br />site {e.site_id}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>
{#if more}
	<p><button class="button" onclick={loadMore} disabled={busy}>{busy ? 'Laden…' : 'Meer laden'}</button></p>
{/if}

<style>
	.nowrap {
		white-space: nowrap;
	}
	.warning .event {
		color: var(--danger);
	}
</style>
