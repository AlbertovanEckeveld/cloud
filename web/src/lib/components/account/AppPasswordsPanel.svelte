<script lang="ts">
	// App passwords for apps that cannot sign in with a passkey, such as
	// WebDAV clients. The password is shown once; only a hash is stored.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, type AppPassword } from '#lib/api.ts';
	import { dateTime, timeAgo } from '#lib/format.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let list = $state<AppPassword[] | null>(null);
	let error = $state<unknown>(null);
	let name = $state('');
	let created = $state<AppPassword | null>(null);

	const base = $derived(page.url.origin);

	async function load() {
		try {
			list = await api.appPasswords();
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function create(e: SubmitEvent) {
		e.preventDefault();
		error = null;
		try {
			created = await api.createAppPassword(name);
			name = '';
			await load();
		} catch (err) {
			error = err;
		}
	}

	async function revoke(a: AppPassword) {
		if (!confirm(`App-wachtwoord "${a.name}" intrekken? Apps die het gebruiken, kunnen dan niet meer bij je bestanden.`)) return;
		error = null;
		try {
			await api.revokeAppPassword(a.id);
			await load();
		} catch (err) {
			error = err;
		}
	}
</script>

<section class="panel" aria-labelledby="app-passwords-title">
	<h2 id="app-passwords-title">App-wachtwoorden</h2>
	<p class="muted small">
		Voor apps die niet met een passkey kunnen inloggen, zoals een WebDAV-client of je bestandsbeheerder. Gebruik je
		gebruikersnaam <code>{page.data.user?.username}</code> met een app-wachtwoord; je gewone wachtwoord werkt daar niet.
	</p>
	<dl class="details small">
		<dt>Eigen bestanden</dt>
		<dd><code>{base}/dav/files/</code></dd>
		<dt>Teammappen</dt>
		<dd><code>{base}/dav/teams/</code></dd>
		<dt>Gedeeld met mij</dt>
		<dd><code>{base}/dav/shared/</code></dd>
	</dl>
	<ErrorNotice {error} />
	{#if created}
		<div class="flash flash--success" role="status">
			<p>Het app-wachtwoord voor <strong>{created.name}</strong>; kopieer het nu, het wordt niet opnieuw getoond:</p>
			<p><code class="secret">{created.password}</code></p>
			<button class="button" onclick={() => created && navigator.clipboard.writeText(created.password)}>Kopiëren</button>
			<button class="button" onclick={() => (created = null)}>Klaar</button>
		</div>
	{/if}
	{#if list && list.length > 0}
		<ul class="list">
			{#each list as a (a.id)}
				<li>
					<div>
						<span class="list__title">{a.name}</span>
						<span class="muted small block">
							aangemaakt {dateTime(a.created_at)} ·
							{#if a.last_used_at}laatst gebruikt <time datetime={a.last_used_at} title={dateTime(a.last_used_at)}>{timeAgo(a.last_used_at)}</time>
							{:else}nog niet gebruikt{/if}
						</span>
					</div>
					<button class="button" onclick={() => revoke(a)}>Intrekken</button>
				</li>
			{/each}
		</ul>
	{/if}
	<form class="add" onsubmit={create}>
		<input bind:value={name} required maxlength="100" placeholder="Naam, bijv. Laptop of Telefoon" aria-label="Naam van het app-wachtwoord" />
		<button class="button button--primary">Aanmaken</button>
	</form>
</section>

<style>
	.secret {
		font-size: 1.15rem;
		letter-spacing: 0.04em;
		user-select: all;
	}
</style>
