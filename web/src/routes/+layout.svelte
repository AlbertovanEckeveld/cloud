<script lang="ts">
	import '../app.css';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();
</script>

<a class="skip-link" href="#main">Naar inhoud</a>

<header class="site-header">
	<div class="container site-header__inner">
		<a class="brand" href="/">Cloud</a>
	</div>
</header>

<main id="main" class="container">
	{@render children()}
</main>

<style>
	.site-header {
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.site-header__inner {
		display: flex;
		align-items: center;
		gap: 1.5rem;
		padding-block: 1rem;
	}
	.brand {
		font-weight: 700;
		color: var(--text-strong);
	}
	.brand:hover {
		text-decoration: none;
	}
	main {
		padding-block: 2.5rem 4rem;
	}
</style>
