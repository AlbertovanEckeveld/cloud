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

async function getJSON<T>(path: string, parse: Validator<T>, fetchFn: typeof fetch): Promise<T> {
	let res: Response;
	try {
		res = await fetchFn(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' });
	} catch {
		throw new ApiError(0, 'network', 'De server is niet bereikbaar');
	}

	let body: unknown;
	try {
		body = await res.json();
	} catch {
		throw new ApiError(res.status, 'invalid_response', 'Onverwacht antwoord van de server');
	}

	if (!res.ok) {
		const err = record(body, 'error').error;
		const detail = typeof err === 'object' && err !== null ? (err as Record<string, unknown>) : {};
		throw new ApiError(
			res.status,
			typeof detail.code === 'string' ? detail.code : 'http_error',
			typeof detail.message === 'string' ? detail.message : `Serverfout (${res.status})`
		);
	}
	return parse(body);
}

export const api = {
	status: (fetchFn: typeof fetch = fetch) => getJSON('/api/v1/status', parseStatus, fetchFn),
	sites: (fetchFn: typeof fetch = fetch) => getJSON('/api/v1/sites', parseSites, fetchFn)
};
