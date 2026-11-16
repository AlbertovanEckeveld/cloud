<script lang="ts">
	// Cloud's own PDF reader on top of PDF.js: pages below each other, zoom,
	// and a page counter. Pages are only drawn when they scroll into view.
	// PDF.js runs in a worker from this origin, without WebAssembly, so the
	// strict CSP stays as it is (ADR 0009).
	import { onDestroy } from 'svelte';
	import { getDocument, GlobalWorkerOptions, type PDFDocumentProxy } from 'pdfjs-dist';
	import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url';
	import Icon from '#lib/components/Icon.svelte';

	GlobalWorkerOptions.workerSrc = workerUrl;

	let { url }: { url: string } = $props();

	let pdf = $state<PDFDocumentProxy | null>(null);
	let error = $state<string | null>(null);
	let zoom = $state(1); // relative to "fit width"
	let current = $state(1);
	let scroller: HTMLDivElement;
	const drawn = new Map<number, number>(); // page -> zoom it was drawn at
	const visible = new Set<number>(); // pages at least partly in view
	let observer: IntersectionObserver | null = null;

	$effect(() => {
		const task = getDocument({ url, useWasm: false, enableXfa: false });
		task.promise.then(
			(doc) => (pdf = doc),
			() => (error = 'Deze PDF kan niet worden geopend.')
		);
		return () => {
			task.destroy();
		};
	});

	async function draw(canvas: HTMLCanvasElement, n: number) {
		if (!pdf || drawn.get(n) === zoom) return;
		drawn.set(n, zoom);
		const page = await pdf.getPage(n);
		const base = page.getViewport({ scale: 1 });
		// Leave room for the padding and a vertical scrollbar.
		const width = Math.max(200, scroller.clientWidth - 72) * zoom;
		const viewport = page.getViewport({ scale: width / base.width });
		const ratio = window.devicePixelRatio || 1;
		canvas.width = Math.floor(viewport.width * ratio);
		canvas.height = Math.floor(viewport.height * ratio);
		// CSSOM properties, not a style attribute: allowed by the CSP.
		canvas.style.width = `${viewport.width}px`;
		canvas.style.height = `${viewport.height}px`;
		await page.render({ canvas, viewport, transform: ratio === 1 ? undefined : [ratio, 0, 0, ratio, 0, 0] }).promise;
	}

	// Attachment for each page canvas: draw it when it comes into view.
	function lazy(canvas: HTMLCanvasElement, n: number) {
		observer ??= new IntersectionObserver(
			(entries) => {
				for (const e of entries) {
					const p = Number((e.target as HTMLElement).dataset.page);
					if (e.isIntersecting) draw(e.target as HTMLCanvasElement, p);
					if (e.intersectionRatio > 0.4) visible.add(p);
					else visible.delete(p);
				}
				// The counter shows the topmost page that is mostly in view.
				if (visible.size > 0) current = Math.min(...visible);
			},
			{ root: scroller, rootMargin: '400px 0px', threshold: [0, 0.4] }
		);
		canvas.dataset.page = String(n);
		observer.observe(canvas);
		return { destroy: () => observer?.unobserve(canvas) };
	}

	function setZoom(z: number) {
		zoom = Math.min(3, Math.max(0.5, z));
		// Redraw what is visible at the new size.
		for (const c of scroller.querySelectorAll<HTMLCanvasElement>('canvas')) {
			observer?.unobserve(c);
			observer?.observe(c);
		}
	}

	// The document itself is released by destroying its loading task (above).
	onDestroy(() => observer?.disconnect());
</script>

<div class="pdf">
	{#if pdf}
		<div class="pdf__bar">
			<span class="muted small">Pagina {current} van {pdf.numPages}</span>
			<button class="icon-button" aria-label="Uitzoomen" title="Uitzoomen" onclick={() => setZoom(zoom - 0.25)}><Icon name="minus" /></button>
			<span class="muted small">{Math.round(zoom * 100)}%</span>
			<button class="icon-button" aria-label="Inzoomen" title="Inzoomen" onclick={() => setZoom(zoom + 0.25)}><Icon name="plus" /></button>
		</div>
	{/if}
	<div class="pdf__pages" bind:this={scroller}>
		{#if error}
			<p class="muted">{error}</p>
		{:else if pdf}
			{#each { length: pdf.numPages } as _, i (i)}
				<canvas use:lazy={i + 1} class="pdf__page" aria-label="Pagina {i + 1}"></canvas>
			{/each}
		{:else}
			<p class="muted">Laden…</p>
		{/if}
	</div>
</div>

<style>
	.pdf {
		display: flex;
		flex-direction: column;
		width: 100%;
		height: 100%;
	}
	.pdf__bar {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		padding: 0.35rem;
		border-bottom: 1px solid var(--border);
		font-size: 1.2rem;
	}
	.pdf__pages {
		flex: 1;
		overflow: auto;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
		padding: 1.5rem;
	}
	.pdf__page {
		flex-shrink: 0; /* keep the page's own proportions */
		background: #fff;
		box-shadow: 0 4px 18px rgb(0 0 0 / 0.4);
		min-height: 200px;
		min-width: 150px;
	}
</style>
