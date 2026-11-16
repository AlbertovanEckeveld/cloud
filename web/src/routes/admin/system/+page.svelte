<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ApiError, type Site, type Status } from '#lib/api.ts';
	import { checkLabel, dateTime, timeAgo } from '#lib/format.ts';

	const refreshEvery = 10_000;

	let status = $state<Status | null>(null);
	let sites = $state<Site[]>([]);
	let error = $state<string | null>(null);
	let loading = $state(false);
	// Ticks every second so relative times ("2 minuten geleden") stay current.
	let now = $state(new Date());

	async function refresh() {
		if (loading) return;
		loading = true;
		try {
			[status, sites] = await Promise.all([api.status(), api.sites()]);
			error = null;
		} catch (e) {
			error = e instanceof ApiError ? e.message : 'Er ging iets mis bij het ophalen van de status';
		} finally {
			loading = false;
			now = new Date();
		}
	}

	onMount(() => {
		refresh();
		const poll = setInterval(() => {
			// Do not poll in a background tab; refresh as soon as it is visible again.
			if (document.visibilityState === 'visible') refresh();
		}, refreshEvery);
		const tick = setInterval(() => (now = new Date()), 1000);
		const onVisible = () => document.visibilityState === 'visible' && refresh();
		document.addEventListener('visibilitychange', onVisible);
		return () => {
			clearInterval(poll);
			clearInterval(tick);
			document.removeEventListener('visibilitychange', onVisible);
		};
	});
</script>

<svelte:head>
	<title>Systeem · Beheer · Cloud</title>
</svelte:head>

<div class="page-header">
	<div>
		<h1>Systeemstatus</h1>
		{#if status}
			<p class="muted">
				Site <code>{status.site_id}</code> · versie <code>{status.version}</code> · gestart
				<time datetime={status.started_at} title={dateTime(status.started_at)}>
					{timeAgo(status.started_at, now)}
				</time>
			</p>
		{/if}
	</div>
	<div class="page-header__actions">
		{#if status}
			<span class="badge" class:badge--ok={status.ok} class:badge--error={!status.ok}>
				{status.ok ? 'Alles werkt' : 'Storing'}
			</span>
		{/if}
		<button class="button" onclick={refresh} disabled={loading}>
			{loading ? 'Bezig…' : 'Vernieuwen'}
		</button>
	</div>
</div>

{#if error}
	<p class="flash flash--error" role="alert">{error}</p>
{/if}

<div class="grid">
	<section class="panel" aria-labelledby="components-title">
		<h2 id="components-title">Componenten</h2>
		{#if status}
			<ul class="checks">
				{#each status.checks as check (check.name)}
					<li>
						<span class="dot" class:dot--ok={check.ok} aria-hidden="true"></span>
						<span class="checks__name">{checkLabel(check.name)}</span>
						<span class="muted checks__latency">{check.latency_ms} ms</span>
						<span class="badge" class:badge--ok={check.ok} class:badge--error={!check.ok}>
							{check.ok ? 'Bereikbaar' : check.optional ? 'Niet beschikbaar' : 'Onbereikbaar'}
						</span>
					</li>
					{#if check.name === 'collabora' && !check.ok}
						<li class="muted small">
							Documenten bewerken staat uit. Collabora is onbereikbaar, of zijn editor-adres (server_name) is niet gelijk
							aan COLLABORA_URL. De precieze reden staat in het serverlog ("health check failed").
						</li>
					{/if}
				{/each}
			</ul>
			<p class="muted small">
				Gecontroleerd <time datetime={status.checked_at} title={dateTime(status.checked_at)}>
					{timeAgo(status.checked_at, now)}
				</time>
			</p>
		{:else if !error}
			<p class="muted">Laden…</p>
		{/if}
	</section>

	<section class="panel" aria-labelledby="sites-title">
		<h2 id="sites-title">Sites</h2>
		{#if sites.length > 0}
			<div class="table-wrap">
				<table class="table">
					<thead>
						<tr><th>Site</th><th>Versie</th><th>Laatst gezien</th><th>Status</th></tr>
					</thead>
					<tbody>
						{#each sites as site (site.id)}
							<tr>
								<td>
									<code>{site.id}</code>
									{#if site.id === status?.site_id}<span class="muted small">(deze)</span>{/if}
								</td>
								<td><code>{site.version}</code></td>
								<td>
									<time datetime={site.last_seen_at} title={dateTime(site.last_seen_at)}>
										{timeAgo(site.last_seen_at, now)}
									</time>
								</td>
								<td>
									<span class="badge" class:badge--ok={site.online} class:badge--error={!site.online}>
										{site.online ? 'Online' : 'Offline'}
									</span>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{:else if !error}
			<p class="muted">Laden…</p>
		{/if}
	</section>
</div>

<style>
	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-end;
		gap: 1rem;
		flex-wrap: wrap;
		margin-bottom: 2rem;
	}
	.page-header p {
		margin: 0;
	}
	.page-header__actions {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.grid {
		display: grid;
		gap: 1rem;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
		align-items: start;
	}
	.checks {
		list-style: none;
		margin: 0 0 0.75rem;
		padding: 0;
	}
	.checks li {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding-block: 0.6rem;
		border-bottom: 1px solid var(--border);
	}
	.checks li:last-child {
		border-bottom: 0;
	}
	.checks__name {
		color: var(--text-strong);
		flex: 1;
	}
	.checks__latency {
		font-family: var(--mono);
		font-size: 0.85rem;
	}
	.dot {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		background: var(--danger);
		flex: none;
	}
	.dot--ok {
		background: var(--accent);
	}
	.small {
		font-size: 0.85rem;
		margin: 0;
	}
</style>
