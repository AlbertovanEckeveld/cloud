<script lang="ts">
	// The desktop app's only window: sign in, choose folders, see what syncs.
	import { onMount } from 'svelte';
	import { bridge, message, summary, type Root, type Status } from './bridge.ts';

	let status = $state<Status | null>(null);
	let roots = $state<Root[]>([]);
	let error = $state('');
	let busy = $state(false);
	let server = $state('https://');
	let insecure = $state(false);

	// The service owns the state; the window asks for it every second.
	async function refresh() {
		try {
			const s = await bridge().Status();
			if (!status?.signed_in && s.signed_in) loadRoots();
			if (!status && s.server) server = s.server;
			status = s;
		} catch (e) {
			error = message(e);
		}
	}

	onMount(() => {
		refresh();
		const timer = setInterval(refresh, 1000);
		return () => clearInterval(timer);
	});

	async function act(fn: () => Promise<unknown>) {
		busy = true;
		error = '';
		try {
			await fn();
			status = await bridge().Status();
		} catch (e) {
			error = message(e);
		} finally {
			busy = false;
		}
	}

	async function loadRoots() {
		try {
			roots = await bridge().Roots();
		} catch (e) {
			error = message(e);
		}
	}

	const signIn = (e: SubmitEvent) => {
		e.preventDefault();
		return act(async () => {
			await bridge().SignIn(server.trim(), insecure);
			await loadRoots();
		});
	};

	const synced = (id: string) => status?.folders.some((f) => f.root === id);
	const total = (f: Status['folders'][number]) =>
		f.report.Uploaded + f.report.Downloaded + f.report.DeletedLocal + f.report.TrashedRemote;
</script>

<main class="container">
	<header class="head">
		<h1>Cloud</h1>
		{#if status}<span class="badge" class:badge--ok={summary(status) === 'Alles bijgewerkt'}>{summary(status)}</span>{/if}
	</header>

	{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
	{#if status?.error}<p class="flash flash--error" role="alert">{status.error}</p>{/if}

	{#if status && !status.signed_in}
		<form class="panel form" onsubmit={signIn}>
			<h2>Koppel deze computer</h2>
			<p class="muted small">Je logt in via je browser, met je passkey. Je wachtwoord komt nooit in deze app.</p>
			<label class="field">
				<span>Adres van je Cloud</span>
				<input bind:value={server} required placeholder="https://cloud.voorbeeld.nl" autocomplete="url" />
			</label>
			{#if server.startsWith('http://')}
				<label class="check">
					<input type="checkbox" bind:checked={insecure} />
					<span>Ik weet dat dit adres geen https gebruikt (alleen voor een ontwikkelserver via een versleuteld netwerk)</span>
				</label>
			{/if}
			<div><button class="button button--primary" disabled={busy}>{busy ? 'Wachten op je browser…' : 'Inloggen via browser'}</button></div>
		</form>
	{:else if status}
		<section class="panel">
			<h2>Gesynchroniseerde mappen</h2>
			{#if status.folders.length === 0}
				<p class="muted">Nog geen mappen. Kies hieronder wat je op deze computer wilt hebben.</p>
			{/if}
			<ul class="list">
				{#each status.folders as f (f.root)}
					<li>
						<div>
							<span class="list__title">{f.name}</span>
							<button class="link-button small block" onclick={() => bridge().OpenFolder(f.dir)}>{f.dir}</button>
							<span class="muted small block">
								{#if f.last_run && !f.last_run.startsWith('0001')}
									laatst {new Date(f.last_run).toLocaleTimeString('nl-NL')}{total(f) ? ` · ${total(f)} wijziging(en)` : ''}{f.report.Conflicts ? ` · ${f.report.Conflicts} conflict(en): beide versies bewaard` : ''}
								{:else}nog niet gesynchroniseerd{/if}
							</span>
							{#if f.error}<span class="small block error">{f.error}</span>{/if}
							{#if f.mass_delete}
								<div class="flash flash--error">
									<p>
										Gepauzeerd: deze ronde zou {f.mass_delete.local} bestand(en) hier en {f.mass_delete.remote} op de server verwijderen
										(van de {f.mass_delete.total}). Klopt dat? Op de server gaat het naar de prullenbak.
									</p>
									<button class="button" onclick={() => act(() => bridge().ConfirmMassDelete(f.root))}>Ja, verwijderen</button>
								</div>
							{/if}
						</div>
						<button class="button" disabled={busy} onclick={() => act(() => bridge().RemoveFolder(f.root))}>Stoppen</button>
					</li>
				{/each}
			</ul>
			{#if roots.some((r) => !synced(r.id))}
				<h3>Toevoegen</h3>
				<ul class="list">
					{#each roots.filter((r) => !synced(r.id)) as r (r.id)}
						<li>
							<span>{r.name} <span class="muted small">{r.kind === 'team' ? 'teammap' : r.kind === 'shared' ? 'gedeeld met mij' : ''}</span></span>
							<button class="button" disabled={busy} onclick={() => act(() => bridge().ChooseAndAdd(r.id, r.name))}>Kies een map…</button>
						</li>
					{/each}
				</ul>
			{/if}
		</section>

		<section class="panel settings">
			<label class="check">
				<input type="checkbox" checked={status.autostart} onchange={(e) => act(() => bridge().SetAutostart(e.currentTarget.checked))} />
				<span>Starten bij inloggen op deze computer</span>
			</label>
			<div class="actions">
				<button class="button" disabled={busy} onclick={() => act(() => bridge().SyncNow())}>Nu synchroniseren</button>
				<button class="button" disabled={busy} onclick={() => act(() => bridge().SetPaused(!status?.paused))}>
					{status.paused ? 'Hervatten' : 'Pauzeren'}
				</button>
				<button class="button" disabled={busy} onclick={() => act(() => bridge().SignOut())}>Ontkoppelen</button>
			</div>
			<p class="muted small">
				Gekoppeld aan {status.server}. Cloud draait op de achtergrond, ook als dit venster dicht is; het icoon in de
				systeembalk toont de status. Afsluiten kan via dat icoon.
			</p>
		</section>
	{/if}
</main>

<style>
	main {
		padding-block: 1.5rem;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 1rem;
	}
	.head h1 {
		margin: 0;
	}
	.list li {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
	}
	.error {
		color: var(--error-text);
	}
	.settings {
		margin-top: 1rem;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin: 0.75rem 0;
	}
</style>
