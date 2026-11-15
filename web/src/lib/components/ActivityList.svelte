<script lang="ts">
	import type { AuditEvent } from '#lib/api.ts';
	import { dateTime, eventLabel, isWarningEvent, timeAgo } from '#lib/format.ts';

	let { events }: { events: AuditEvent[] } = $props();
</script>

{#if events.length === 0}
	<p class="muted">Nog geen activiteit.</p>
{:else}
	<ul class="activity">
		{#each events as event (event.id)}
			<li class:warning={isWarningEvent(event.event)}>
				<span class="activity__what">{eventLabel(event.event)}</span>
				<span class="muted small">
					<time datetime={event.occurred_at} title={dateTime(event.occurred_at)}>{timeAgo(event.occurred_at)}</time>
					{#if event.ip}· {event.ip}{/if}
				</span>
			</li>
		{/each}
	</ul>
{/if}

<style>
	.activity {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.activity li {
		display: flex;
		flex-direction: column;
		padding-block: 0.5rem;
		border-bottom: 1px solid var(--border);
	}
	.activity li:last-child {
		border-bottom: 0;
	}
	.activity__what {
		color: var(--text-strong);
	}
	.warning .activity__what {
		color: var(--danger);
	}
</style>
