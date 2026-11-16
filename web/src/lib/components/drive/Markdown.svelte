<script lang="ts">
	// Renders markdown without ever inserting HTML: marked only parses, and
	// every token becomes a Svelte element whose text Svelte escapes. HTML
	// inside the markdown is shown as text, links only go to http(s) or mailto,
	// and images become links (loading them would reveal the reader to
	// whatever server hosts them).
	import { lexer, type Token, type Tokens } from 'marked';

	let { source }: { source: string } = $props();

	const tokens = $derived(lexer(source));

	function safeHref(href: string): string | null {
		try {
			const u = new URL(href, 'https://invalid.example/');
			if (u.origin === 'https://invalid.example') return null; // relative: no target here
			return ['http:', 'https:', 'mailto:'].includes(u.protocol) ? u.href : null;
		} catch {
			return null;
		}
	}
</script>

{#snippet inline(list: Token[])}
	{#each list as t, i (i)}
		{#if t.type === 'strong'}<strong>{@render inline((t as Tokens.Strong).tokens)}</strong>
		{:else if t.type === 'em'}<em>{@render inline((t as Tokens.Em).tokens)}</em>
		{:else if t.type === 'del'}<del>{@render inline((t as Tokens.Del).tokens)}</del>
		{:else if t.type === 'codespan'}<code>{(t as Tokens.Codespan).text}</code>
		{:else if t.type === 'br'}<br />
		{:else if t.type === 'checkbox'}<input class="task" type="checkbox" checked={(t as Tokens.Checkbox).checked} disabled aria-label="Taak" />
		{:else if t.type === 'link' || t.type === 'image'}
			{@const href = safeHref((t as Tokens.Link).href)}
			{#if href}
				<a {href} target="_blank" rel="noopener noreferrer">
					{#if t.type === 'image'}🖼 {(t as Tokens.Image).text || href}{:else}{@render inline((t as Tokens.Link).tokens)}{/if}
				</a>
			{:else}{t.type === 'image' ? (t as Tokens.Image).text : (t as Tokens.Link).text}{/if}
		{:else if 'tokens' in t && t.tokens}{@render inline(t.tokens)}
		{:else if t.type === 'escape' || t.type === 'text'}{(t as Tokens.Text).text}
		{:else}{t.raw}{/if}
	{/each}
{/snippet}

{#snippet blocks(list: Token[])}
	{#each list as t, i (i)}
		{#if t.type === 'heading'}
			{@const h = t as Tokens.Heading}
			<svelte:element this={`h${Math.min(h.depth + 1, 6)}`}>{@render inline(h.tokens)}</svelte:element>
		{:else if t.type === 'paragraph'}<p>{@render inline((t as Tokens.Paragraph).tokens)}</p>
		{:else if t.type === 'code'}<pre><code>{(t as Tokens.Code).text}</code></pre>
		{:else if t.type === 'blockquote'}<blockquote>{@render blocks((t as Tokens.Blockquote).tokens)}</blockquote>
		{:else if t.type === 'hr'}<hr />
		{:else if t.type === 'list'}
			{@const l = t as Tokens.List}
			<svelte:element this={l.ordered ? 'ol' : 'ul'}>
				{#each l.items as item, j (j)}
					<li>
						{@render blocks(item.tokens)}
					</li>
				{/each}
			</svelte:element>
		{:else if t.type === 'table'}
			{@const tb = t as Tokens.Table}
			<div class="table-wrap">
				<table class="table">
					<thead><tr>{#each tb.header as c, j (j)}<th>{@render inline(c.tokens)}</th>{/each}</tr></thead>
					<tbody>
						{#each tb.rows as row, j (j)}<tr>{#each row as c, k (k)}<td>{@render inline(c.tokens)}</td>{/each}</tr>{/each}
					</tbody>
				</table>
			</div>
		{:else if t.type === 'text'}
			{@const tx = t as Tokens.Text}
			{#if tx.tokens}{@render inline(tx.tokens)}{:else}{tx.text}{/if}
		{:else if t.type === 'checkbox'}<input class="task" type="checkbox" checked={(t as Tokens.Checkbox).checked} disabled aria-label="Taak" />
		{:else if t.type !== 'space'}<p class="raw">{t.raw}</p>{/if}
	{/each}
{/snippet}

<article class="markdown">{@render blocks(tokens)}</article>

<style>
	.markdown {
		max-width: 80ch;
		margin: 0 auto;
		padding: 1.5rem 2rem;
		line-height: 1.65;
		align-self: flex-start;
		width: 100%;
	}
	.markdown :global(pre) {
		background: var(--surface-2);
		padding: 0.75rem 1rem;
		border-radius: 8px;
		overflow-x: auto;
	}
	.markdown :global(blockquote) {
		margin: 0;
		padding-left: 1rem;
		border-left: 3px solid var(--border);
		color: var(--muted);
	}
	.markdown :global(li > p) {
		margin: 0.2rem 0;
	}
	.markdown :global(input.task) {
		width: auto;
		margin: 0 0.45rem 0 0;
		vertical-align: -0.1em;
	}
	.markdown :global(li:has(input.task)) {
		list-style: none;
	}
	.raw {
		white-space: pre-wrap;
	}
</style>
