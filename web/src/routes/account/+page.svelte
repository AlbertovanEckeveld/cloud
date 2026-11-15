<script lang="ts">
	import { onMount } from 'svelte';
	import { api, errorMessage, type AuditEvent } from '#lib/api.ts';
	import { dateTime, eventLabel, isWarningEvent, timeAgo } from '#lib/format.ts';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	let events = $state<AuditEvent[] | null>(null);
	let activityError = $state<string | null>(null);

	let current = $state('');
	let next = $state('');
	let confirmation = $state('');
	let passwordError = $state<string | null>(null);
	let passwordSaved = $state(false);
	let busy = $state(false);

	async function loadActivity() {
		try {
			events = await api.activity();
			activityError = null;
		} catch (err) {
			activityError = errorMessage(err);
		}
	}

	onMount(loadActivity);

	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		passwordError = null;
		passwordSaved = false;
		if (next !== confirmation) {
			passwordError = 'De nieuwe wachtwoorden zijn niet gelijk.';
			return;
		}
		busy = true;
		try {
			await api.changePassword(current, next);
			passwordSaved = true;
			current = next = confirmation = '';
			await loadActivity();
		} catch (err) {
			passwordError = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Account · Cloud</title>
</svelte:head>

<h1>Account</h1>

<div class="grid">
	<div class="stack">
		<section class="panel" aria-labelledby="profile-title">
			<h2 id="profile-title">Profiel</h2>
			{#if data.user}
				<dl class="details">
					<dt>Naam</dt>
					<dd>{data.user.display_name}</dd>
					<dt>Gebruikersnaam</dt>
					<dd><code>{data.user.username}</code></dd>
					<dt>Rol</dt>
					<dd>{data.user.is_admin ? 'Beheerder' : 'Gebruiker'}</dd>
					<dt>Tweestapsverificatie</dt>
					<dd><span class="badge badge--ok">Authenticator-app actief</span></dd>
				</dl>
			{/if}
		</section>

		<section class="panel" aria-labelledby="password-title">
			<h2 id="password-title">Wachtwoord wijzigen</h2>
			<p class="muted small">Na het wijzigen word je op al je andere apparaten uitgelogd.</p>
			{#if passwordError}<p class="flash flash--error" role="alert">{passwordError}</p>{/if}
			{#if passwordSaved}<p class="flash flash--success" role="status">Je wachtwoord is gewijzigd.</p>{/if}
			<form class="form" onsubmit={changePassword}>
				<!-- Hidden username field lets password managers update the right entry. -->
				<input class="visually-hidden" value={data.user?.username} autocomplete="username" readonly tabindex="-1" aria-hidden="true" />
				<label class="field">
					<span>Huidig wachtwoord</span>
					<input type="password" bind:value={current} autocomplete="current-password" required />
				</label>
				<label class="field">
					<span>Nieuw wachtwoord</span>
					<input type="password" bind:value={next} autocomplete="new-password" minlength="12" required />
				</label>
				<label class="field">
					<span>Herhaal nieuw wachtwoord</span>
					<input type="password" bind:value={confirmation} autocomplete="new-password" required />
				</label>
				<div>
					<button class="button button--primary" disabled={busy}>{busy ? 'Bezig…' : 'Opslaan'}</button>
				</div>
			</form>
		</section>
	</div>

	<section class="panel" aria-labelledby="activity-title">
		<h2 id="activity-title">Recente activiteit</h2>
		<p class="muted small">Herken je iets niet? Wijzig dan direct je wachtwoord.</p>
		{#if activityError}
			<p class="flash flash--error" role="alert">{activityError}</p>
		{:else if events === null}
			<p class="muted">Laden…</p>
		{:else}
			<ul class="activity">
				{#each events as event (event.id)}
					<li class:warning={isWarningEvent(event.event)}>
						<span class="activity__what">{eventLabel(event.event)}</span>
						<span class="muted small">
							<time datetime={event.occurred_at} title={dateTime(event.occurred_at)}>
								{timeAgo(event.occurred_at)}
							</time>
							{#if event.ip}· {event.ip}{/if}
						</span>
					</li>
				{/each}
			</ul>
		{/if}
	</section>
</div>

<style>
	.grid {
		display: grid;
		gap: 1rem;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
		align-items: start;
		margin-top: 1.5rem;
	}
	.stack {
		display: grid;
		gap: 1rem;
	}
	.details {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.4rem 1rem;
		margin: 0;
	}
	.details dt {
		color: var(--muted);
	}
	.details dd {
		margin: 0;
	}
	.small {
		font-size: 0.85rem;
		margin: 0 0 1rem;
	}
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
	.activity .small {
		margin: 0;
	}
	.activity__what {
		color: var(--text-strong);
	}
	.warning .activity__what {
		color: var(--danger);
	}
	.visually-hidden {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
	}
</style>
