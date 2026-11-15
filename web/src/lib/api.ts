// Typed client for the platform API.
//
// TypeScript types disappear at runtime, so every response is validated before
// the UI uses it: a buggy or compromised backend must not be able to put
// unexpected shapes into the page.

export class ApiError extends Error {
	constructor(
		readonly status: number,
		readonly code: string,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

export interface CheckResult {
	name: string;
	ok: boolean;
	latency_ms: number;
}

export interface Status {
	site_id: string;
	version: string;
	started_at: string;
	ok: boolean;
	checks: CheckResult[];
	checked_at: string;
}

export interface Site {
	id: string;
	version: string;
	first_seen_at: string;
	last_seen_at: string;
	online: boolean;
}

export interface User {
	id: string;
	username: string;
	display_name: string;
	is_admin: boolean;
}

export type SessionState = 'mfa_required' | 'mfa_enrollment_required' | 'authenticated';

export interface Session {
	state: SessionState;
	user: User | null;
}

export interface TotpEnrollment {
	secret: string;
	uri: string;
	/** PNG QR code as a data: URI. */
	qr: string;
}

export interface AuditEvent {
	id: string;
	occurred_at: string;
	event: string;
	site_id: string;
	ip: string;
	user_agent: string;
}

type Validator<T> = (v: unknown) => T;

function fail(what: string): never {
	throw new ApiError(0, 'invalid_response', `Onverwacht antwoord van de server (${what})`);
}

function record(v: unknown, what: string): Record<string, unknown> {
	if (typeof v !== 'object' || v === null || Array.isArray(v)) fail(what);
	return v as Record<string, unknown>;
}

function str(o: Record<string, unknown>, key: string): string {
	const v = o[key];
	if (typeof v !== 'string') fail(key);
	return v;
}

function bool(o: Record<string, unknown>, key: string): boolean {
	const v = o[key];
	if (typeof v !== 'boolean') fail(key);
	return v;
}

function num(o: Record<string, unknown>, key: string): number {
	const v = o[key];
	if (typeof v !== 'number' || !Number.isFinite(v)) fail(key);
	return v;
}

function list<T>(o: Record<string, unknown>, key: string, item: Validator<T>): T[] {
	const v = o[key];
	if (!Array.isArray(v)) fail(key);
	return v.map(item);
}

const sessionStates: readonly string[] = ['mfa_required', 'mfa_enrollment_required', 'authenticated'];

function parseState(o: Record<string, unknown>): SessionState {
	const state = str(o, 'state');
	if (!sessionStates.includes(state)) fail('state');
	return state as SessionState;
}

export const parseStatus: Validator<Status> = (v) => {
	const o = record(v, 'status');
	return {
		site_id: str(o, 'site_id'),
		version: str(o, 'version'),
		started_at: str(o, 'started_at'),
		ok: bool(o, 'ok'),
		checks: list(o, 'checks', (c) => {
			const r = record(c, 'check');
			return { name: str(r, 'name'), ok: bool(r, 'ok'), latency_ms: num(r, 'latency_ms') };
		}),
		checked_at: str(o, 'checked_at')
	};
};

export const parseSites: Validator<Site[]> = (v) =>
	list(record(v, 'sites'), 'sites', (s) => {
		const r = record(s, 'site');
		return {
			id: str(r, 'id'),
			version: str(r, 'version'),
			first_seen_at: str(r, 'first_seen_at'),
			last_seen_at: str(r, 'last_seen_at'),
			online: bool(r, 'online')
		};
	});

export const parseUser: Validator<User> = (v) => {
	const o = record(v, 'user');
	return {
		id: str(o, 'id'),
		username: str(o, 'username'),
		display_name: str(o, 'display_name'),
		is_admin: bool(o, 'is_admin')
	};
};

export const parseSession: Validator<Session> = (v) => {
	const o = record(v, 'session');
	const state = parseState(o);
	// The server only sends user details for a fully authenticated session.
	const user = state === 'authenticated' ? parseUser(o.user) : null;
	return { state, user };
};

const parseStateOnly: Validator<SessionState> = (v) => parseState(record(v, 'session'));

const parseEnrollment: Validator<TotpEnrollment> = (v) => {
	const o = record(v, 'enrollment');
	const uri = str(o, 'uri');
	if (!uri.startsWith('otpauth://')) fail('uri');
	const qr = str(o, 'qr');
	if (!qr.startsWith('data:image/png;base64,')) fail('qr');
	return { secret: str(o, 'secret'), uri, qr };
};

export const parseActivity: Validator<AuditEvent[]> = (v) =>
	list(record(v, 'activity'), 'events', (e) => {
		const r = record(e, 'event');
		return {
			id: str(r, 'id'),
			occurred_at: str(r, 'occurred_at'),
			event: str(r, 'event'),
			site_id: str(r, 'site_id'),
			ip: str(r, 'ip'),
			user_agent: str(r, 'user_agent')
		};
	});

const noContent: Validator<void> = () => undefined;

async function request<T>(
	method: 'GET' | 'POST',
	path: string,
	parse: Validator<T>,
	body: unknown,
	fetchFn: typeof fetch
): Promise<T> {
	const headers: Record<string, string> = { Accept: 'application/json' };
	if (body !== undefined) headers['Content-Type'] = 'application/json';

	let res: Response;
	try {
		res = await fetchFn(path, {
			method,
			headers,
			body: body === undefined ? undefined : JSON.stringify(body),
			credentials: 'same-origin'
		});
	} catch {
		throw new ApiError(0, 'network', 'De server is niet bereikbaar');
	}
	if (res.status === 204) return parse(undefined);

	let data: unknown;
	try {
		data = await res.json();
	} catch {
		throw new ApiError(res.status, 'invalid_response', 'Onverwacht antwoord van de server');
	}

	if (!res.ok) {
		const err = record(data, 'error').error;
		const detail = typeof err === 'object' && err !== null ? (err as Record<string, unknown>) : {};
		throw new ApiError(
			res.status,
			typeof detail.code === 'string' ? detail.code : 'http_error',
			typeof detail.message === 'string' ? detail.message : `Serverfout (${res.status})`
		);
	}
	return parse(data);
}

const get = <T>(path: string, parse: Validator<T>, f: typeof fetch) =>
	request('GET', path, parse, undefined, f);
const post = <T>(path: string, parse: Validator<T>, body: unknown, f: typeof fetch) =>
	request('POST', path, parse, body, f);

export const api = {
	status: (f: typeof fetch = fetch) => get('/api/v1/status', parseStatus, f),
	sites: (f: typeof fetch = fetch) => get('/api/v1/sites', parseSites, f),

	/** The current session, or null when not signed in. */
	session: async (f: typeof fetch = fetch): Promise<Session | null> => {
		try {
			return await get('/api/v1/auth/session', parseSession, f);
		} catch (e) {
			if (e instanceof ApiError && e.status === 401) return null;
			throw e;
		}
	},
	login: (username: string, password: string, f: typeof fetch = fetch) =>
		post('/api/v1/auth/login', parseStateOnly, { username, password }, f),
	loginTotp: (code: string, f: typeof fetch = fetch) =>
		post('/api/v1/auth/login/totp', parseStateOnly, { code }, f),
	activate: (token: string, password: string, f: typeof fetch = fetch) =>
		post('/api/v1/auth/activate', parseStateOnly, { token, password }, f),
	beginTotpEnrollment: (f: typeof fetch = fetch) =>
		post('/api/v1/auth/totp/enrollment', parseEnrollment, undefined, f),
	confirmTotpEnrollment: (code: string, f: typeof fetch = fetch) =>
		post('/api/v1/auth/totp/enrollment/confirm', parseStateOnly, { code }, f),
	logout: (f: typeof fetch = fetch) => post('/api/v1/auth/logout', noContent, {}, f),

	activity: (f: typeof fetch = fetch) => get('/api/v1/account/activity', parseActivity, f),
	changePassword: (currentPassword: string, newPassword: string, f: typeof fetch = fetch) =>
		post(
			'/api/v1/account/password',
			noContent,
			{ current_password: currentPassword, new_password: newPassword },
			f
		)
};

/** The message to show for a failed API call. */
export function errorMessage(e: unknown): string {
	return e instanceof ApiError ? e.message : 'Er ging iets mis. Probeer het opnieuw.';
}
