<script lang="ts">
	// The assistant: questions about your own files, answered with sources
	// (ADR 0023). It only reads files you can open; it changes nothing.
	import { onMount, tick } from 'svelte';
	import { page } from '$app/state';
	import Icon from '#lib/components/Icon.svelte';
	import Markdown from '#lib/components/drive/Markdown.svelte';
	import PreviewDialog from '#lib/components/drive/PreviewDialog.svelte';
	import {
		api,
		errorMessage,
		type AnswerSource,
		type AssistantStatus,
		type ChatMessage,
		type Conversation
	} from '#lib/api.ts';
	import { editable } from '#lib/drive-state.svelte.ts';
	import { previewKind, timeAgo } from '#lib/format.ts';
	import { can } from '#lib/permissions.ts';

	let status = $state<AssistantStatus | null>(null);
	let conversations = $state<Conversation[]>([]);
	let current = $state<string | null>(null);
	let messages = $state<ChatMessage[]>([]);
	let question = $state('');
	let pending = $state<{ question: string; answer: string } | null>(null);
	let error = $state('');
	let stop: AbortController | null = null;
	let log = $state<HTMLElement>();
	let viewing = $state<AnswerSource | null>(null);

	const mayManage = $derived(can(page.data.user, 'assistant.provider.manage'));

	onMount(async () => {
		try {
			[status, conversations] = await Promise.all([api.assistant.status(), api.assistant.conversations()]);
			if (conversations[0]) await open(conversations[0].id);
		} catch (e) {
			error = errorMessage(e);
		}
	});

	async function scrollDown() {
		await tick();
		log?.scrollTo({ top: log.scrollHeight });
	}

	async function open(id: string) {
		error = '';
		try {
			const c = await api.assistant.conversation(id);
			current = id;
			messages = c.messages;
			await scrollDown();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	function startNew() {
		current = null;
		messages = [];
		error = '';
	}

	async function remove(c: Conversation) {
		if (!confirm(`Gesprek "${c.title}" verwijderen?`)) return;
		try {
			await api.assistant.deleteConversation(c.id);
			conversations = conversations.filter((x) => x.id !== c.id);
			if (current === c.id) startNew();
		} catch (e) {
			error = errorMessage(e);
		}
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
			if (!current) {
				const c = await api.assistant.createConversation();
				current = c.id;
				conversations = [c, ...conversations];
			}
			const reply = await api.assistant.ask(current, q, (text) => {
				if (pending) pending.answer += text;
				scrollDown();
			}, stop.signal);
			messages = [...messages, { id: `q-${reply.id}`, role: 'user', content: q, sources: [], created_at: reply.created_at }, reply];
			conversations = await api.assistant.conversations();
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

	// A source opens in the viewer when it can, else in its folder.
	function sourceHref(s: AnswerSource): string | null {
		if (s.kind === 'folder') return `/files/${s.id}`;
		if (previewKind(s.content_type, s.name)) return null;
		if (editable(s.name)) return `/edit/${s.id}`;
		return s.location_id ? `/files/${s.location_id}` : '/files';
	}
</script>

<svelte:head>
	<title>Assistent · Cloud</title>
</svelte:head>

<div class="assistant">
	<aside class="threads" aria-label="Gesprekken">
		<button class="button button--primary" onclick={startNew}><span class="with-icon"><Icon name="plus" />Nieuw gesprek</span></button>
		<ul>
			{#each conversations as c (c.id)}
				<li class:active={c.id === current}>
					<button class="link-button thread" onclick={() => open(c.id)}>
						<span class="thread__title">{c.title}</span>
						<span class="muted small">{timeAgo(c.updated_at)}</span>
					</button>
					<button class="icon-button" aria-label="Gesprek verwijderen" title="Verwijderen" onclick={() => remove(c)}><Icon name="trash" size={16} /></button>
				</li>
			{/each}
		</ul>
		{#if mayManage}<a class="small with-icon" href="/assistant/settings"><Icon name="settings" size={16} />AI-instellingen</a>{/if}
	</aside>

	<section class="chat">
		{#if status && !status.chat}
			<div class="panel empty">
				<h1>Assistent</h1>
				<p class="muted">De assistent is nog niet ingesteld: er is geen taalmodel gekozen.</p>
				{#if mayManage}<p><a href="/assistant/settings">Naar de AI-instellingen</a></p>{:else}<p class="muted small">Vraag een beheerder om de assistent in te stellen.</p>{/if}
			</div>
		{:else}
			{#if status?.external_chat}
				<p class="flash external" role="note">
					Let op: je vragen en de passende stukken uit je bestanden gaan naar <strong>{status.external_chat}</strong>, buiten je eigen netwerk.
				</p>
			{/if}
			<div class="log" bind:this={log} aria-live="polite">
				{#if messages.length === 0 && !pending}
					<div class="intro">
						<h1>Wat wil je weten?</h1>
						<p class="muted">
							Vraag iets over je bestanden: "Wat kost de offerte voor de dakkapel?", "Waar staat het huurcontract?" of
							"Vat de notulen van maart samen". De assistent leest alleen bestanden die jij mag openen en verandert niets.
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
										{@const href = sourceHref(s)}
										<li value={s.n}>
											{#if href}
												<a class="with-icon" {href}><Icon name={s.kind === 'folder' ? 'folder' : 'file'} size={16} />{s.name}</a>
											{:else}
												<button class="link-button with-icon" onclick={() => (viewing = s)}><Icon name="file" size={16} />{s.name}</button>
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
			<form class="ask" onsubmit={ask}>
				<label class="visually-hidden" for="question">Je vraag</label>
				<textarea id="question" bind:value={question} onkeydown={keys} rows="2" maxlength="4000"
					placeholder="Stel een vraag over je bestanden…" disabled={!!pending}></textarea>
				{#if pending}
					<button type="button" class="button" onclick={() => stop?.abort()}>Stoppen</button>
				{:else}
					<button class="button button--primary" disabled={!question.trim()}>Vragen</button>
				{/if}
			</form>
		{/if}
	</section>
</div>

{#if viewing}
	<PreviewDialog files={[viewing]} start={0} onClose={() => (viewing = null)} />
{/if}

<style>
	.assistant {
		display: grid;
		grid-template-columns: 260px minmax(0, 1fr);
		gap: 2rem;
		align-items: start;
	}
	.threads {
		position: sticky;
		top: 1rem;
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.threads ul {
		list-style: none;
		padding: 0;
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		max-height: 60vh;
		overflow-y: auto;
	}
	.threads li {
		display: flex;
		align-items: center;
		border-radius: 10px;
	}
	.threads li.active {
		background: var(--surface-2);
	}
	.thread {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		padding: 0.45rem 0.6rem;
		color: var(--text);
		text-align: left;
	}
	.thread:hover {
		text-decoration: none;
	}
	.thread__title {
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.chat {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		min-height: 70vh;
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
		.assistant {
			grid-template-columns: minmax(0, 1fr);
			gap: 1rem;
		}
		.threads {
			position: static;
		}
		.threads ul {
			max-height: 9rem;
		}
		.msg {
			max-width: 100%;
		}
	}
</style>
