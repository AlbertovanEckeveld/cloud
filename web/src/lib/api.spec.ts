import { describe, expect, it } from 'vitest';
import { api, ApiError } from './api.ts';

const status = {
	site_id: 'home',
	version: 'v1',
	started_at: '2026-11-15T10:00:00Z',
	ok: true,
	checks: [{ name: 'database', ok: true, latency_ms: 3, optional: false }],
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

describe('drive sharing', () => {
	const node = {
		id: 'n1',
		parent_id: null,
		kind: 'folder',
		name: 'Vakantie',
		size: 0,
		content_type: '',
		created_at: '2026-11-15T10:00:00Z',
		updated_at: '2026-11-15T10:00:00Z',
		permission: 'edit'
	};

	it('validates the permission of a node', async () => {
		const view = await api.drive.folder('n1', respond({ folder: node, path: [], children: [], shared: true }));
		expect(view.folder).toMatchObject({ permission: 'edit', shared: false });
		expect(view.shared).toBe(true);
		const bad = { folder: { ...node, permission: 'owner' }, path: [], children: [], shared: true };
		await expect(api.drive.folder('n1', respond(bad))).rejects.toMatchObject({ code: 'invalid_response' });
	});

	it('lists what is shared with me, with the owner', async () => {
		const list = await api.drive.sharedWithMe(respond({ nodes: [{ ...node, owner_name: 'Eigenaar' }] }));
		expect(list[0]).toMatchObject({ name: 'Vakantie', owner_name: 'Eigenaar' });
	});

	it('rejects a share with an unknown subject type', async () => {
		const share = { subject: { type: 'user', id: 'u1' }, name: 'Kijker', permission: 'view', granted_at: '2026-11-15T10:00:00Z' };
		expect(await api.drive.shares('n1', respond({ shares: [share] }))).toHaveLength(1);
		const bad = { ...share, subject: { type: 'everyone', id: 'u1' } };
		await expect(api.drive.shares('n1', respond({ shares: [bad] }))).rejects.toMatchObject({ code: 'invalid_response' });
	});

	it('accepts only avatar URLs in the directory', async () => {
		const entry = { type: 'user', id: 'u1', name: 'Kijker', detail: 'kijker', picture: '' };
		expect(await api.directory('kij', respond({ entries: [entry] }))).toHaveLength(1);
		const bad = { ...entry, picture: 'javascript:alert(1)' };
		await expect(api.directory('kij', respond({ entries: [bad] }))).rejects.toMatchObject({ code: 'invalid_response' });
	});

	it('encodes the subject in the unshare URL', async () => {
		let url = '';
		const capture: typeof fetch = async (input) => {
			url = String(input);
			return new Response(null, { status: 204 });
		};
		await api.drive.unshare('n1', { type: 'group', id: 'a/b' }, capture);
		expect(url).toBe('/api/v1/drive/nodes/n1/shares/group/a%2Fb');
	});
});

describe('team folders', () => {
	const folder = {
		id: 't1',
		root_id: 'r1',
		name: 'Familie',
		groups: [{ subject: { type: 'group', id: 'g1' }, name: 'familie', permission: 'edit', granted_at: '2026-11-16T10:00:00Z' }],
		usage: { used_bytes: 4, limit_bytes: null },
		created_at: '2026-11-16T10:00:00Z',
		deleted_at: null
	};

	it('validates the administrator overview', async () => {
		const list = await api.admin.teamFolders(respond({ team_folders: [folder] }));
		expect(list[0]).toMatchObject({ name: 'Familie', deleted_at: null, usage: { used_bytes: 4 } });
		const bad = { ...folder, groups: [{ ...folder.groups[0], permission: 'owner' }] };
		await expect(api.admin.teamFolders(respond({ team_folders: [bad] }))).rejects.toMatchObject({ code: 'invalid_response' });
	});

	it('returns the team of a team trash', async () => {
		const trash = await api.drive.teamTrash('t1', respond({ team: { id: 't1', root_id: 'r1', name: 'Familie' }, nodes: [] }));
		expect(trash.team.name).toBe('Familie');
	});
});

describe('share links', () => {
	it('accepts only a well-formed token', async () => {
		const link = { id: 'l1', token: 'A'.repeat(43), password: false, expires_at: null, created_at: '2026-11-16T10:00:00Z', downloads: 0 };
		expect((await api.drive.createLink('n1', { expires_at: null, password: '' }, respond(link, 201))).token).toHaveLength(43);
		await expect(api.drive.createLink('n1', { expires_at: null, password: '' }, respond({ ...link, token: 'x/../y' }, 201))).rejects.toMatchObject({
			code: 'invalid_response'
		});
	});
});

describe('search', () => {
	it('validates results with their location', async () => {
		const r = { id: 'n1', parent_id: null, kind: 'file', name: 'rapport.pdf', size: 1, content_type: 'application/pdf',
			created_at: '2026-11-16T10:00:00Z', updated_at: '2026-11-16T10:00:00Z', permission: 'view', location: 'Werk', location_id: 'f1' };
		expect((await api.drive.search('rapp', respond({ results: [r] })))[0]).toMatchObject({ location: 'Werk', location_id: 'f1' });
		await expect(api.drive.search('rapp', respond({ results: [{ ...r, location: 3 }] }))).rejects.toMatchObject({ code: 'invalid_response' });
	});
});

describe('api.activity', () => {
	it('asks for a page and returns whether more follow', async () => {
		let asked = '';
		const f: typeof fetch = async (input) => {
			asked = String(input);
			return new Response(JSON.stringify({ events: [], has_more: true }), { headers: { 'Content-Type': 'application/json' } });
		};
		await expect(api.activity('0193-last', 5, f)).resolves.toEqual({ events: [], has_more: true });
		expect(asked).toBe('/api/v1/account/activity?limit=5&before=0193-last');
	});

	it('rejects a page without has_more', async () => {
		await expect(api.activity(null, 5, respond({ events: [] }))).rejects.toBeInstanceOf(ApiError);
	});
});

describe('api.devices', () => {
	const device = { id: 'd1', name: 'werk-laptop', platform: 'linux', linked_at: '2026-11-17T10:00:00Z', last_seen_at: null };

	it('returns validated devices, name and platform optional', async () => {
		const unnamed = { ...device, id: 'd2', name: null, platform: null };
		await expect(api.devices(respond({ devices: [device, unnamed] }))).resolves.toEqual([device, unnamed]);
	});

	it('rejects an unknown platform', async () => {
		await expect(api.devices(respond({ devices: [{ ...device, platform: 'amiga' }] }))).rejects.toBeInstanceOf(ApiError);
	});

	it('returns what devices synchronise', async () => {
		const sync = { device_id: 'd1', last_sync_at: '2026-11-17T10:05:00Z', folders: [{ id: 'mine', name: 'Mijn bestanden', kind: 'mine' }] };
		await expect(api.drive.syncDevices(respond({ devices: [sync] }))).resolves.toEqual([sync]);
		await expect(api.drive.syncDevices(respond({ devices: [{ device_id: 'd1' }] }))).rejects.toBeInstanceOf(ApiError);
	});
});
