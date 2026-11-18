// A Svelte action for <details> used as a pop-up menu: it closes when you
// click outside it, press Escape, or choose one of its items.
export function dismissible(node: HTMLDetailsElement) {
	const close = () => (node.open = false);
	const outside = (e: PointerEvent) => {
		if (node.open && !node.contains(e.target as Node)) close();
	};
	const key = (e: KeyboardEvent) => {
		if (node.open && e.key === 'Escape') {
			close();
			node.querySelector('summary')?.focus();
		}
	};
	const chosen = (e: MouseEvent) => {
		if ((e.target as Element).closest('[role="menuitem"]')) close();
	};
	document.addEventListener('pointerdown', outside);
	document.addEventListener('keydown', key);
	node.addEventListener('click', chosen);
	return {
		destroy() {
			document.removeEventListener('pointerdown', outside);
			document.removeEventListener('keydown', key);
			node.removeEventListener('click', chosen);
		}
	};
}
