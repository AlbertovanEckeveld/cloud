import { describe, expect, it } from 'vitest';
import { pageOf } from './paging.ts';

describe('pageOf', () => {
	const seven = [1, 2, 3, 4, 5, 6, 7];

	it('slices pages of the given size', () => {
		expect(pageOf(seven, 0, 3)).toEqual({ items: [1, 2, 3], page: 0, pages: 3, label: '1–3 van 7' });
		expect(pageOf(seven, 2, 3)).toEqual({ items: [7], page: 2, pages: 3, label: '7 van 7' });
	});

	it('clamps pages that no longer exist, e.g. after removing the last item', () => {
		expect(pageOf(seven, 9, 3).page).toBe(2);
		expect(pageOf(seven, -1, 3).page).toBe(0);
		expect(pageOf(seven.slice(0, 3), 1, 3)).toMatchObject({ items: [1, 2, 3], page: 0, pages: 1 });
	});

	it('handles an empty list', () => {
		expect(pageOf([], 0, 3)).toEqual({ items: [], page: 0, pages: 1, label: '0 van 0' });
	});
});
