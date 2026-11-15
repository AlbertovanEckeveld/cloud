<script lang="ts">
	// The user's own profile: picture, names and language. Apps that sign in
	// through Cloud receive these as claims (name, given_name, picture, …).
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { api, type Profile } from '#lib/api.ts';
	import ErrorNotice from '../ErrorNotice.svelte';

	let profile = $state<Profile | null>(null);
	let displayName = $state('');
	let givenName = $state('');
	let familyName = $state('');
	let locale = $state<'nl' | 'en'>('nl');
	let error = $state<unknown>(null);
	let saved = $state(false);
	let busy = $state(false);

	function show(p: Profile) {
		profile = p;
		displayName = p.display_name;
		givenName = p.given_name;
		familyName = p.family_name;
		locale = p.locale;
	}

	onMount(async () => {
		try {
			show(await api.profile());
		} catch (e) {
			error = e;
		}
	});

	async function act(action: () => Promise<Profile>) {
		busy = true;
		error = null;
		saved = false;
		try {
			show(await action());
			saved = true;
			await invalidateAll(); // the header shows the display name
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}

	function save(e: SubmitEvent) {
		e.preventDefault();
		return act(() => api.updateProfile({ display_name: displayName, given_name: givenName, family_name: familyName, locale }));
	}

	function upload(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (file) return act(() => api.uploadAvatar(file));
	}

	const initials = $derived(
		displayName
			.split(/\s+/)
			.filter(Boolean)
			.slice(0, 2)
			.map((w) => w[0].toUpperCase())
			.join('')
	);
</script>

<ErrorNotice {error} />
{#if saved}<p class="flash flash--success" role="status">Opgeslagen.</p>{/if}

{#if profile}
	<div class="avatar-row">
		{#if profile.picture}
			<img class="avatar" src={profile.picture} alt="Profielfoto" />
		{:else}
			<span class="avatar avatar--empty" aria-hidden="true">{initials}</span>
		{/if}
		<div class="avatar-actions">
			<label class="button">
				{profile.picture ? 'Andere foto' : 'Foto toevoegen'}
				<input type="file" accept="image/png,image/jpeg" onchange={upload} disabled={busy} hidden />
			</label>
			{#if profile.picture}
				<button class="button" onclick={() => act(() => api.removeAvatar())} disabled={busy}>Verwijderen</button>
			{/if}
			<span class="muted small block">PNG of JPEG, max. 5 MB. Wordt vierkant gemaakt; locatiegegevens worden verwijderd.</span>
		</div>
	</div>

	<form class="form" onsubmit={save}>
		<label class="field">
			<span>Weergavenaam</span>
			<input bind:value={displayName} required maxlength="100" autocomplete="name" />
		</label>
		<div class="row">
			<label class="field">
				<span>Voornaam</span>
				<input bind:value={givenName} maxlength="100" autocomplete="given-name" />
			</label>
			<label class="field">
				<span>Achternaam</span>
				<input bind:value={familyName} maxlength="100" autocomplete="family-name" />
			</label>
		</div>
		<label class="field">
			<span>Taal</span>
			<select bind:value={locale}>
				<option value="nl">Nederlands</option>
				<option value="en">English</option>
			</select>
		</label>
		<div><button class="button button--primary" disabled={busy}>Opslaan</button></div>
	</form>
{/if}

<style>
	.avatar-row {
		display: flex;
		align-items: center;
		gap: 1rem;
		margin-bottom: 1.25rem;
	}
	.avatar {
		width: 72px;
		height: 72px;
		border-radius: 50%;
		object-fit: cover;
		flex: none;
	}
	.avatar--empty {
		display: grid;
		place-items: center;
		background: var(--surface-3);
		color: var(--text-strong);
		font-weight: 700;
		font-size: 1.4rem;
	}
	.avatar-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
	}
	@media (max-width: 520px) {
		.row {
			grid-template-columns: 1fr;
		}
	}
</style>
