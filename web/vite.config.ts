import { defineConfig } from 'vitest/config';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';

// The Go backend this dev server proxies to (see `make dev`).
const backend = 'http://127.0.0.1:8080';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// Single-page app: every unknown path serves index.html and the
			// client-side router takes over. The Go binary embeds the output.
			adapter: adapter({ fallback: 'index.html' })
		})
	],
	server: {
		proxy: {
			'/api': backend,
			'/healthz': backend,
			'/readyz': backend
		}
	},
	test: {
		expect: { requireAssertions: true },
		include: ['src/**/*.{test,spec}.ts'],
		environment: 'node'
	}
});
