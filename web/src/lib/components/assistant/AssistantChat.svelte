<script lang="ts">
	// One conversation with the assistant: messages with their sources, the
	// answer streaming in, and the question box. Used on /assistant and in
	// the panel every page can open (ADR 0024). Questions can be about items
	// (focus): only references go to the server, which checks access itself.
	import { onMount, tick } from 'svelte';
	import { page } from '$app/state';
	import Icon from '#lib/components/Icon.svelte';
	import Markdown from '#lib/components/drive/Markdown.svelte';
	import PreviewDialog from '#lib/components/drive/PreviewDialog.svelte';
	import {
		api,
		driveDetail,
		errorMessage,
		type AnswerSource,
		type AssistantStatus,
		type ChatMessage,
		type Conversation,
		type SearchResult
	} from '#lib/api.ts';
	import type { Focus } from '#lib/assistant-state.svelte.ts';
	import { editable } from '#lib/drive-state.svelte.ts';
	import { previewKind } from '#lib/format.ts';
	import { can } from '#lib/permissions.ts';

	let {
		conversation = $bindable(null),
		focus = [],
		initial = null,
		compact = false,
		onFocusCleared,
		onInitialSent,
		onConversation
	}: {
		conversation?: string | null;
		focus?: Focus[];
		initial?: string | null;
		compact?: boolean;
		onFocusCleared?: () => void;
		onInitialSent?: () => void;
		onConversation?: (c: Conversation) => void;
	} = $props();

	let status = $state<AssistantStatus | null>(null);
	let messages = $state<ChatMessage[]>([]);
	let loaded = $state<string | null>(null);
	let question = $state('');
	let pending = $state<{ question: string; answer: string } | null>(null);
	let error = $state('');
	let stop: AbortController | null = null;
	let log = $state<HTMLElement>();
	let viewing = $state<SearchResult | null>(null);

	const mayManage = $derived(can(page.data.user, 'assistant.provider.manage'));

	onMount(async () => {
		try {
			status = await api.assistant.status();
		} catch (e) {
			error = errorMessage(e);
		}
	});

	// Show the conversation that is chosen (not one this view just created).
	$effect(() => {
		const id = conversation;
		if (id === loaded || pending) return;
		loaded = id;
		if (!id) {
			messages = [];
			return;
		}
		api.assistant
			.conversation(id)
			.then((c) => {
				if (conversation === id) {
					messages = c.messages;
					scrollDown();
				}
			})
			.catch((e) => (error = errorMessage(e)));
	});

	// A question handed over by another page is sent once the status is known.
	$effect(() => {
		if (initial && status?.chat && !pending) {
			question = initial;
			onInitialSent?.();
			ask();
		}
	});

	async function scrollDown() {
		await tick();
		log?.scrollTo({ top: log.scrollHeight });
	}

	async function ask(e?: Event) {
		e?.preventDefault();
		const q = question.trim();
		if (!q || pending) return;
		error = '';
		pending = { question: q, answer: '' };
		question = '';
		stop = new AbortController();
		await scrollDown();
		try {
			if (!conversation) {
				const c = await api.assistant.createConversation();
				loaded = c.id;
				conversation = c.id;
				onConversation?.(c);
			}
			const reply = await api.assistant.ask(
				conversation,
				q,
				(text) => {
					if (pending) pending.answer += text;
					scrollDown();
				},
				stop.signal,
				focus
			);
			messages = [...messages, { id: `q-${reply.id}`, role: 'user', content: q, sources: [], created_at: reply.created_at }, reply];
			onConversation?.({ id: conversation, title: '', created_at: reply.created_at, updated_at: reply.created_at });
		} catch (err) {
			if (!stop?.signal.aborted) error = errorMessage(err);
			question = q; // give the question back to try again
		} finally {
			pending = null;
			stop = null;
			await scrollDown();
		}
	}

	function keys(e: KeyboardEvent) {
		// Enter sends, Shift+Enter is a new line.
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) ask(e);
	}

	// The sources an answer cites ([1], [2]); all of them if it cites none.
	function cited(m: ChatMessage): AnswerSource[] {
		const numbers = new Set([...m.content.matchAll(/\[(\d+)\]/g)].map((x) => Number(x[1])));
		const some = m.sources.filter((s) => numbers.has(s.n));
		return some.length > 0 ? some : m.sources;
	}

	// A file opens in the viewer when it can; anything else on its own page.
	function previewable(s: AnswerSource): SearchResult | null {
		const file = driveDetail(s);
		return file && file.kind === 'file' && previewKind(file.content_type, file.name) ? file : null;
	}

	// Documents open in the editor, everything else on its own page.
	function href(s: AnswerSource): string {
		const file = driveDetail(s);
		return file && file.kind === 'file' && editable(file.name) ? `/edit/${file.id}` : s.link;
	}
</script>

<div class="chat" class:compact>
	{#if status && !status.chat}
		<div class="empty">
			<p class="muted">De assistent is nog niet ingesteld: er is geen taalmodel gekozen.</p>
			{#if mayManage}<p><a href="/assistant/settings">Naar de AI-instellingen</a></p>{:else}<p class="muted small">Vraag een beheerder om de assistent in te stellen.</p>{/if}
		</div>
	{:else}
		{#if status?.external_chat}
			<p class="flash external" role="note">
				Je vragen en passende stukken uit je bestanden gaan naar <strong>{status.external_chat}</strong>, buiten je eigen netwerk.
			</p>
		{/if}
		<div class="log" bind:this={log} aria-live="polite">
			{#if messages.length === 0 && !pending}
				<div class="intro">
					{#if !compact}<h1>Wat wil je weten?</h1>{/if}
					<p class="muted">
						Vraag iets over je bestanden: "Wat kost de offerte voor de dakkapel?", "Waar staat het huurcontract?" of "Vat de
						notulen van maart samen". De assistent leest alleen wat jij mag openen en verandert niets.
					</p>
				</div>
			{/if}
			{#each messages as m (m.id)}
				<article class="msg msg--{m.role}">
					{#if m.role === 'assistant'}
						<Markdown source={m.content} />
						{#if m.sources.length > 0}
							<ol class="sources" aria-label="Bronnen">
								{#each cited(m) as s (s.n)}
									{@const file = previewable(s)}
									<li value={s.n}>
										{#if file}
											<button class="link-button with-icon" onclick={() => (viewing = file)}><Icon name="file" size={16} />{s.title}</button>
										{:else}
											<a class="with-icon" href={href(s)}><Icon name={s.kind === 'folder' ? 'folder' : 'file'} size={16} />{s.title}</a>
										{/if}
										<span class="muted small">{s.location}</span>
									</li>
								{/each}
							</ol>
						{/if}
					{:else}
						<p>{m.content}</p>
					{/if}
				</article>
			{/each}
			{#if pending}
				<article class="msg msg--user"><p>{pending.question}</p></article>
				<article class="msg msg--assistant">
					{#if pending.answer}<Markdown source={pending.answer} />{:else}<p class="muted">Zoeken in je bestanden…</p>{/if}
				</article>
			{/if}
		</div>
		{#if error}<p class="flash flash--error" role="alert">{error}</p>{/if}
		{#if focus.length > 0}
			<div class="focus" aria-label="Waar de vraag over gaat">
				{#each focus as f (f.source + f.id)}
					<span class="badge with-icon"><Icon name={f.kind === 'folder' ? 'folder' : 'file'} size={14} />{f.title}</span>
				{/each}
				{#if onFocusCleared}
					<button class="icon-button" aria-label="Niet meer over deze items vragen" title="Loskoppelen" onclick={onFocusCleared}><Icon name="close" size={16} /></button>
				{/if}
			</div>
		{/if}
		<form class="ask" onsubmit={ask}>
			<label class="visually-hidden" for="question-{compact ? 'panel' : 'page'}">Je vraag</label>
			<textarea id="question-{compact ? 'panel' : 'page'}" bind:value={question} onkeydown={keys} rows="2" maxlength="4000"
				placeholder={focus.length ? 'Stel een vraag hierover…' : 'Stel een vraag over je bestanden…'} disabled={!!pending}></textarea>
			{#if pending}
				<button type="button" class="button" onclick={() => stop?.abort()}>Stoppen</button>
			{:else}
				<button class="button button--primary" disabled={!question.trim()}>Vragen</button>
			{/if}
		</form>
	{/if}
</div>

{#if viewing}
	<PreviewDialog files={[viewing]} start={0} onClose={() => (viewing = null)} />
{/if}

<style>
	.chat {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		min-height: 70vh;
	}
	.chat.compact {
		min-height: 0;
		height: 100%;
	}
	.log {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: 1rem;
		max-height: 65vh;
		overflow-y: auto;
		padding-right: 0.25rem;
	}
	.compact .log {
		max-height: none;
		min-height: 0;
	}
	.intro {
		margin: auto 0;
		max-width: 60ch;
	}
	.msg {
		max-width: 85%;
		padding: 0.75rem 1rem;
		border-radius: var(--radius);
		overflow-wrap: anywhere;
	}
	.compact .msg {
		max-width: 100%;
	}
	/* The document reader's spacing is for pages; in a chat bubble it is too much. */
	.msg :global(.markdown) {
		padding: 0;
		max-width: none;
	}
	.msg :global(p:first-child) {
		margin-top: 0;
	}
	.msg :global(p:last-child) {
		margin-bottom: 0;
	}
	.msg--user {
		align-self: flex-end;
		background: var(--surface-2);
		white-space: pre-wrap;
	}
	.msg--assistant {
		align-self: flex-start;
		background: var(--surface);
		border: 1px solid var(--border);
	}
	.sources {
		margin: 0.75rem 0 0;
		padding: 0.6rem 0 0 1.4rem;
		border-top: 1px solid var(--border);
		font-size: 0.9rem;
	}
	.sources li + li {
		margin-top: 0.25rem;
	}
	.sources span {
		margin-left: 0.4rem;
	}
	.focus {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		align-items: center;
	}
	.ask {
		display: flex;
		gap: 0.5rem;
		align-items: flex-end;
	}
	.ask textarea {
		flex: 1;
		resize: vertical;
		min-height: 2.8rem;
	}
	.external {
		margin: 0;
	}
	@media (max-width: 760px) {
		.msg {
			max-width: 100%;
		}
	}
</style>
