<script module lang="ts">
	// Line icons on a 24×24 grid, drawn for this project. Shapes only: colour
	// follows the text (currentColor), so icons work in every theme.
	const paths = {
		folder: ['M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z'],
		file: ['M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z', 'M14 3v5h5'],
		share: ['M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', 'M2 21v-1a6 6 0 0 1 12 0v1', 'M19 8v6', 'M16 11h6'],
		history: ['M3 12a9 9 0 1 0 3-6.7L3 8', 'M3 3v5h5', 'M12 7v5l3 2'],
		rename: ['M4 20h4L19 9l-4-4L4 16z', 'M13.5 6.5l4 4'],
		move: ['M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z', 'M9 13h7', 'M13 10l3 3-3 3'],
		trash: ['M4 7h16', 'M10 11v6', 'M14 11v6', 'M6 7l1 13h10l1-13', 'M9 7V4h6v3'],
		download: ['M12 4v12', 'M7 11l5 5 5-5', 'M5 20h14'],
		upload: ['M12 20V8', 'M7 13l5-5 5 5', 'M5 4h14'],
		plus: ['M12 5v14', 'M5 12h14'],
		minus: ['M5 12h14'],
		edit: ['M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z', 'M14 3v5h5', 'M9 17l1-3 5-5 2 2-5 5z'],
		back: ['M19 12H5', 'M11 6l-6 6 6 6'],
		search: ['M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14z', 'M20 20l-4-4'],
		close: ['M6 6l12 12', 'M18 6L6 18'],
		left: ['M15 5l-7 7 7 7'],
		right: ['M9 5l7 7-7 7'],
		users: ['M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', 'M2 21v-1a6 6 0 0 1 12 0v1', 'M16 3.5a4 4 0 0 1 0 7.5', 'M22 21v-1a6 6 0 0 0-4-5.6'],
		home: ['M3 11l9-8 9 8', 'M5 10v10h14V10'],
		cloud: ['M7 18h10a4 4 0 0 0 .5-8A6 6 0 0 0 6 9.5 4.5 4.5 0 0 0 7 18z'],
		restore: ['M3 12a9 9 0 1 0 3-6.7L3 8', 'M3 3v5h5'],
		link: ['M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1', 'M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1'],
		more: ['M5 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2z', 'M12 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2z', 'M19 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2z'],
		menu: ['M4 6h16', 'M4 12h16', 'M4 18h16']
	} as const;

	export type IconName = keyof typeof paths;
</script>

<script lang="ts">
	let { name, size = 18 }: { name: IconName; size?: number } = $props();
</script>

<svg
	class="icon"
	width={size}
	height={size}
	viewBox="0 0 24 24"
	fill="none"
	stroke="currentColor"
	stroke-width="1.8"
	stroke-linecap="round"
	stroke-linejoin="round"
	aria-hidden="true"
	focusable="false"
>
	{#each paths[name] as d (d)}<path {d} />{/each}
</svg>

<style>
	.icon {
		flex-shrink: 0;
		vertical-align: -0.2em;
	}
</style>
