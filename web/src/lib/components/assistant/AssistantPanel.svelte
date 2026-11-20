<script lang="ts">
	// The assistant beside every page (ADR 0024): not a modal, so you can
	// keep working in your files while it is open. Same conversations as
	// /assistant; questions can be about what the page handed over (focus).
	import { tick } from 'svelte';
	import Icon from '#lib/components/Icon.svelte';
	import AssistantChat from './AssistantChat.svelte';
	import { assistant } from '#lib/assistant-state.svelte.ts';

	let panel = $state<HTMLElement>();

	// Put the cursor in the question box when the panel opens.
	$effect(() => {
		if (assistant.open) tick().then(() => panel?.querySelector('textarea')?.focus());
	});

	function keys(e: KeyboardEvent) {
		if (e.key === 'Escape') assistant.open = false;
	}

	function startNew() {
		assistant.conversation = null;
		assistant.focus = [];
	}
</script>

{#if assistant.open}
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<aside class="panel-assistant" bind:this={panel} aria-label="Assistent" onkeydown={keys}>
		<header>
			<h2 class="with-icon"><Icon name="spark" />Assistent</h2>
			<span class="actions">
				<button class="icon-button" aria-label="Nieuw gesprek" title="Nieuw gesprek" onclick={startNew}><Icon name="plus" /></button>
				<a class="icon-button" href="/assistant" aria-label="Als pagina openen" title="Als pagina openen" onclick={() => (assistant.open = false)}><Icon name="right" /></a>
				<button class="icon-button" aria-label="Sluiten" title="Sluiten (Esc)" onclick={() => (assistant.open = false)}><Icon name="close" /></button>
			</span>
		</header>
		<div class="body">
			<AssistantChat
				compact
				bind:conversation={assistant.conversation}
				focus={assistant.focus}
				initial={assistant.ask}
				onInitialSent={() => (assistant.ask = null)}
				onFocusCleared={() => (assistant.focus = [])}
			/>
		</div>
	</aside>
{/if}

<style>
	.panel-assistant {
		position: fixed;
		top: 0;
		right: 0;
		bottom: 0;
		z-index: 40;
		width: min(460px, 100vw);
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border-left: 1px solid var(--border);
		box-shadow: -12px 0 32px rgb(0 0 0 / 0.35);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.75rem 1rem;
		border-bottom: 1px solid var(--border);
	}
	header h2 {
		margin: 0;
		font-size: 1.05rem;
	}
	.actions {
		display: flex;
		gap: 0.15rem;
	}
	.body {
		flex: 1;
		min-height: 0;
		padding: 1rem;
		display: flex;
		flex-direction: column;
	}
	.body > :global(*) {
		flex: 1;
		min-height: 0;
	}
</style>
