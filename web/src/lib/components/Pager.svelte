<script lang="ts">
	// "Vorige / volgende" under a list. Shows nothing when everything fits
	// on one page.
	import Icon from './Icon.svelte';

	let {
		label = '',
		canPrev,
		canNext,
		onprev,
		onnext
	}: { label?: string; canPrev: boolean; canNext: boolean; onprev: () => void; onnext: () => void } = $props();
</script>

{#if canPrev || canNext}
	<nav class="pager" aria-label="Pagina's">
		<button class="icon-button" aria-label="Vorige" title="Vorige" disabled={!canPrev} onclick={onprev}><Icon name="left" /></button>
		{#if label}<span class="muted small" aria-live="polite">{label}</span>{/if}
		<button class="icon-button" aria-label="Volgende" title="Volgende" disabled={!canNext} onclick={onnext}><Icon name="right" /></button>
	</nav>
{/if}

<style>
	.pager {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}
	.pager button:disabled {
		opacity: 0.35;
		cursor: default;
		background: none;
	}
</style>
