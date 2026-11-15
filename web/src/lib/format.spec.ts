import { describe, expect, it } from 'vitest';
import { checkLabel, eventLabel, groupSecret, safeNext, timeAgo } from './format.ts';

const now = new Date('2026-11-15T12:00:00Z');

describe('timeAgo', () => {
	it.each([
		['2026-11-15T11:59:55Z', 'zojuist'],
		['2026-11-15T11:59:30Z', '30 seconden geleden'],
		['2026-11-15T11:58:00Z', '2 minuten geleden'],
		['2026-11-15T09:00:00Z', '3 uur geleden'],
		['2026-11-14T12:00:00Z', 'gisteren'],
		['not a date', 'onbekend']
	])('%s → %s', (iso, want) => {
		expect(timeAgo(iso, now)).toBe(want);
	});
});

describe('checkLabel', () => {
	it('translates known checks and passes through unknown ones', () => {
		expect(checkLabel('storage')).toBe('Objectopslag (S3)');
		expect(checkLabel('nats')).toBe('nats');
	});
});

describe('safeNext', () => {
	const origin = 'https://cloud.example.com';
	it.each([
		['/account', '/account'],
		['/system?tab=sites#top', '/system?tab=sites#top'],
		[null, '/'],
		['', '/'],
		['https://evil.example', '/'],
		['//evil.example/path', '/'],
		['/\\evil.example', '/'],
		['javascript:alert(1)', '/'],
		['account', '/']
	])('%s → %s', (next, want) => {
		expect(safeNext(next, origin)).toBe(want);
	});
});

describe('eventLabel', () => {
	it('translates known events and passes through unknown ones', () => {
		expect(eventLabel('auth.login.failed')).toBe('Mislukte inlogpoging');
		expect(eventLabel('x.y')).toBe('x.y');
	});
});

describe('groupSecret', () => {
	it('groups in blocks of four', () => {
		expect(groupSecret('ABCDEFGHIJ')).toBe('ABCD EFGH IJ');
	});
});
