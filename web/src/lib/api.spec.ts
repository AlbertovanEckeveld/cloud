import { describe, expect, it } from 'vitest';
import { api, ApiError } from './api.ts';

const status = {
	site_id: 'home',
	version: 'v1',
	started_at: '2026-11-15T10:00:00Z',
	ok: true,
	checks: [{ name: 'database', ok: true, latency_ms: 3 }],
	checked_at: '2026-11-15T10:00:05Z'
};

function respond(body: unknown, status = 200): typeof fetch {
	return async () =>
		new Response(typeof body === 'string' ? body : JSON.stringify(body), {
			status,
			headers: { 'Content-Type': 'application/json' }
		});
}

describe('api.status', () => {
	it('returns a validated status', async () => {
		expect(await api.status(respond(status))).toEqual(status);
	});

	it('drops unknown fields', async () => {
		const result = await api.status(respond({ ...status, injected: '<script>' }));
		expect(result).not.toHaveProperty('injected');
	});

	it('rejects a malformed response', async () => {
		const bad = { ...status, checks: [{ name: 'database', ok: 'yes', latency_ms: 3 }] };
		await expect(api.status(respond(bad))).rejects.toMatchObject({ code: 'invalid_response' });
	});

	it('rejects non-JSON bodies', async () => {
		await expect(api.status(respond('<html>proxy error</html>', 502))).rejects.toMatchObject({
			status: 502,
			code: 'invalid_response'
		});
	});

	it('maps API errors', async () => {
		const err = await api
			.status(respond({ error: { code: 'unavailable', message: 'Niet beschikbaar' } }, 503))
			.catch((e: unknown) => e);
		expect(err).toBeInstanceOf(ApiError);
		expect(err).toMatchObject({ status: 503, code: 'unavailable', message: 'Niet beschikbaar' });
	});

	it('maps network failures', async () => {
		const offline: typeof fetch = async () => {
			throw new TypeError('Failed to fetch');
		};
		await expect(api.status(offline)).rejects.toMatchObject({ code: 'network' });
	});
});

describe('api.sites', () => {
	it('returns validated sites', async () => {
		const site = {
			id: 'home',
			version: 'v1',
			first_seen_at: '2026-11-01T00:00:00Z',
			last_seen_at: '2026-11-15T10:00:00Z',
			online: true
		};
		expect(await api.sites(respond({ sites: [site] }))).toEqual([site]);
	});

	it('rejects a missing list', async () => {
		await expect(api.sites(respond({}))).rejects.toMatchObject({ code: 'invalid_response' });
	});
});
