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

describe('api.session', () => {
	const user = { id: 'u1', username: 'alice', display_name: 'Alice', is_admin: false, email: '' };

	it('returns the user for an authenticated session', async () => {
		expect(await api.session(respond({ state: 'authenticated', user }))).toEqual({
			state: 'authenticated',
			user
		});
	});

	it('never exposes a user for a partial session', async () => {
		const session = await api.session(respond({ state: 'mfa_required', user }));
		expect(session).toEqual({ state: 'mfa_required', user: null });
	});

	it('returns null when not signed in', async () => {
		const res = respond({ error: { code: 'unauthenticated', message: 'x' } }, 401);
		expect(await api.session(res)).toBeNull();
	});

	it('rejects an unknown state', async () => {
		await expect(api.session(respond({ state: 'superuser' }))).rejects.toMatchObject({
			code: 'invalid_response'
		});
	});
});

describe('POST requests', () => {
	it('send JSON with the right content type', async () => {
		let seen: RequestInit | undefined;
		const capture: typeof fetch = async (_input, init) => {
			seen = init;
			return new Response(JSON.stringify({ state: 'mfa_required' }), { status: 200 });
		};
		expect(await api.login('alice', 'pw', capture)).toBe('mfa_required');
		expect(seen?.method).toBe('POST');
		expect(new Headers(seen?.headers).get('Content-Type')).toBe('application/json');
		expect(JSON.parse(String(seen?.body))).toEqual({ username: 'alice', password: 'pw' });
	});

	it('accept 204 No Content', async () => {
		const empty: typeof fetch = async () => new Response(null, { status: 204 });
		await expect(api.logout(empty)).resolves.toBeUndefined();
	});

	it('reject an enrollment URI that is not otpauth', async () => {
		const res = respond({ secret: 'ABC', uri: 'javascript:alert(1)', qr: 'data:image/png;base64,AA' });
		await expect(api.beginTotpEnrollment(res)).rejects.toMatchObject({ code: 'invalid_response' });
	});

	it('reject a QR code that is not a PNG data URI', async () => {
		const svg = 'data:image/svg+xml;base64,PHN2Zz4=';
		const res = respond({ secret: 'ABC', uri: 'otpauth://totp/x', qr: svg });
		await expect(api.beginTotpEnrollment(res)).rejects.toMatchObject({ code: 'invalid_response' });
	});
});

describe('admin.invite', () => {
	const bob = { username: 'bob', display_name: 'Bob', is_admin: false, email: '' };

	it('returns a well-formed activation link', async () => {
		const link = 'https://cloud.example.com/activate#abc_DEF-123';
		const res = respond({ user: {}, activation_link: link, emailed: true }, 201);
		expect(await api.admin.invite(bob, res)).toEqual({ link, emailed: true });
	});

	it.each(['javascript:alert(1)//activate#x', 'https://evil.example/phish', 'https://a.b/activate#x y'])(
		'rejects %s',
		async (link) => {
			const res = respond({ user: {}, activation_link: link, emailed: false }, 201);
			await expect(api.admin.invite(bob, res)).rejects.toMatchObject({
				code: 'invalid_response'
			});
		}
	);
});

describe('oidcApprove', () => {
	it('follows only the provider callback', async () => {
		const ok = 'https://cloud.example.com/oidc/authorize/callback?id=01a1e0bb-7af8-7a51-9dde-6451f89621e9';
		expect(await api.oidcApprove('x', respond({ redirect: ok }))).toBe(ok);
	});

	it.each([
		'https://cloud.example.com/oidc/authorize/callback?id=x&next=//evil.example',
		'javascript:alert(1)',
		'https://cloud.example.com/somewhere-else'
	])('rejects %s', async (redirect) => {
		await expect(api.oidcApprove('x', respond({ redirect }))).rejects.toMatchObject({ code: 'invalid_response' });
	});
});

describe('profile', () => {
	const profile = { display_name: 'Bob', given_name: 'Bob', family_name: 'B', locale: 'nl', picture: '' };

	it('accepts an avatar URL', async () => {
		const picture = 'https://cloud.example.com/avatars/0123456789abcdef0123456789abcdef.jpg';
		expect((await api.profile(respond({ ...profile, picture }))).picture).toBe(picture);
	});

	it.each(['javascript:alert(1)', 'https://evil.example/x.jpg', 'https://cloud.example.com/avatars/../../etc.jpg'])(
		'rejects picture %s',
		async (picture) => {
			await expect(api.profile(respond({ ...profile, picture }))).rejects.toMatchObject({ code: 'invalid_response' });
		}
	);

	it('maps upload errors', async () => {
		const res = respond({ error: { code: 'invalid_image', message: 'Geen afbeelding' } }, 422);
		const file = new File(['x'], 'a.png', { type: 'image/png' });
		await expect(api.uploadAvatar(file, res)).rejects.toMatchObject({ status: 422, code: 'invalid_image' });
	});
});
