// Paging through a list that is already complete in the browser (sessions,
// apps, devices: short lists). Long lists page on the server instead.

export interface Page<T> {
	items: T[];
	/** 0-based, clamped to the pages that exist. */
	page: number;
	pages: number;
	/** "1–3 van 7", for screen readers and the pager label. */
	label: string;
}

export function pageOf<T>(all: readonly T[], page: number, size: number): Page<T> {
	const pages = Math.max(1, Math.ceil(all.length / size));
	const p = Math.min(Math.max(0, Math.trunc(page)), pages - 1);
	const start = p * size;
	const items = all.slice(start, start + size);
	const range = items.length === 1 ? `${start + 1}` : `${start + 1}–${start + items.length}`;
	const label = all.length === 0 ? '0 van 0' : `${range} van ${all.length}`;
	return { items, page: p, pages, label };
}
