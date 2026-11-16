import { describe, expect, it } from 'vitest';
import { allows, checkLabel, describeUserAgent, eventLabel, formatBytes, groupSecret, permissionLabel, previewKind, snippetParts, safeNext, timeAgo, uniqueName } from './format.ts';

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

describe('describeUserAgent', () => {
	it.each([
		['Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0', 'Firefox op Linux'],
		['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0', 'Edge op Windows'],
		['Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1', 'Safari op iOS'],
		['Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36', 'Chrome op Android'],
		['cli', 'Opdrachtregel'],
		['curl/8.0', 'Onbekende browser']
	])('%s', (ua, want) => {
		expect(describeUserAgent(ua)).toBe(want);
	});
});

describe('formatBytes', () => {
	it.each([
		[0, '0 B'],
		[1023, '1.023 B'],
		[1536, '1,5 KB'],
		[10 * 1024 * 1024, '10 MB'],
		[10 * 1024 ** 3, '10 GB']
	])('%d → %s', (n, want) => {
		expect(formatBytes(n)).toBe(want);
	});
});

describe('uniqueName', () => {
	it('adds a number before the extension, ignoring case', () => {
		const taken = new Set(['Foto.jpg', 'foto (2).jpg', 'map']);
		expect(uniqueName('nieuw.txt', taken)).toBe('nieuw.txt');
		expect(uniqueName('foto.JPG', taken)).toBe('foto (3).JPG');
		expect(uniqueName('Map', taken)).toBe('Map (2)');
		expect(uniqueName('.bashrc', new Set(['.bashrc']))).toBe('.bashrc (2)');
	});
});

describe('permissions', () => {
	it('orders view < edit < manage', () => {
		expect(allows('manage', 'edit')).toBe(true);
		expect(allows('edit', 'edit')).toBe(true);
		expect(allows('view', 'edit')).toBe(false);
		expect(permissionLabel('edit')).toBe('Kan bewerken');
	});
});

describe('previewKind', () => {
	it('follows the server whitelist', () => {
		expect(previewKind('image/png')).toBe('image');
		expect(previewKind('image/svg+xml')).toBeNull();
		expect(previewKind('text/html; charset=utf-8')).toBe('text');
		expect(previewKind('application/pdf')).toBe('pdf');
		expect(previewKind('text/plain; charset=utf-8', 'LEESMIJ.md')).toBe('markdown');
		expect(previewKind('application/zip', 'x.md')).toBeNull();
		expect(previewKind('video/mp4')).toBe('video');
	});
});

describe('snippetParts', () => {
	it('marks hits and keeps HTML as text', () => {
		expect(snippetParts('de \x02begrotingen\x03 voor <b>2027</b>')).toEqual([
			{ text: 'de ', hit: false },
			{ text: 'begrotingen', hit: true },
			{ text: ' voor <b>2027</b>', hit: false }
		]);
	});
});
