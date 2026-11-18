<script lang="ts">
	// Computers where the desktop app is linked: what they are called, when
	// they were last active, what they keep in sync, and unlinking one. The
	// devices come from identity, what they sync from drive; a failure of
	// the latter only leaves out the folders.
	import { onMount } from 'svelte';
	import { api, type Device, type SyncDevice } from '#lib/api.ts';
	import { dateTime, timeAgo } from '#lib/format.ts';
	import { pageOf } from '#lib/paging.ts';
	import ErrorNotice from '../ErrorNotice.svelte';
	import Pager from '../Pager.svelte';

	let devices = $state<Device[] | null>(null);
	let synced = $state<Map<string, SyncDevice>>(new Map());
	let error = $state<unknown>(null);
	let page = $state(0);
	const shown = $derived(pageOf(devices ?? [], page, 3));

	const platforms: Record<string, string> = { linux: 'Linux', darwin: 'macOS', windows: 'Windows' };

	async function load() {
		try {
			const [list, sync] = await Promise.all([api.devices(), api.drive.syncDevices().catch(() => [] as SyncDevice[])]);
			devices = list;
			synced = new Map(sync.map((d) => [d.device_id, d]));
		} catch (e) {
			error = e;
		}
	}
	onMount(load);

	async function unlink(d: Device) {
		if (!confirm(`${d.name ?? 'Deze computer'} ontkoppelen? Synchroniseren stopt direct; de bestanden op die computer blijven staan.`)) return;
		error = null;
		try {
			await api.unlinkDevice(d.id);
			await load();
		} catch (e) {
			error = e;
		}
	}
</script>

<section class="panel" aria-labelledby="devices-title">
	<h2 id="devices-title">Gekoppelde computers</h2>
	<p class="muted small">Computers waarop de Cloud-app je bestanden synchroniseert.</p>
	<ErrorNotice {error} />
	{#if devices && devices.length > 0}
		<ul class="list">
			{#each shown.items as d (d.id)}
				{@const s = synced.get(d.id)}
				<li>
					<div>
						<span class="list__title">{d.name ?? 'Computer zonder naam'}</span>
						{#if d.platform}<span class="badge">{platforms[d.platform]}</span>{/if}
						<span class="muted small block">
							{#if d.last_seen_at}actief <time datetime={d.last_seen_at} title={dateTime(d.last_seen_at)}>{timeAgo(d.last_seen_at)}</time> ·{/if}
							gekoppeld <time datetime={d.linked_at} title={dateTime(d.linked_at)}>{timeAgo(d.linked_at)}</time>
						</span>
						<span class="muted small block">
							{#if s && s.folders.length > 0}
								Synchroniseert {s.folders.map((f) => f.name).join(', ')} · laatste ronde
								<time datetime={s.last_sync_at} title={dateTime(s.last_sync_at)}>{timeAgo(s.last_sync_at)}</time>
							{:else if s}
								Geen mappen gekozen
							{:else}
								Nog niet gesynchroniseerd
							{/if}
						</span>
					</div>
					<button class="button" onclick={() => unlink(d)}>Ontkoppelen</button>
				</li>
			{/each}
		</ul>
		<Pager label={shown.label} canPrev={shown.page > 0} canNext={shown.page < shown.pages - 1}
			onprev={() => (page = shown.page - 1)} onnext={() => (page = shown.page + 1)} />
	{:else if devices}
		<p class="muted">Nog geen computers gekoppeld. Installeer de Cloud-app en log daarin in.</p>
	{/if}
</section>
