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
	/** Empty when the user has no e-mail address. */
	email: string;
}

export interface Activation {
	link: string;
	/** True when the link was also mailed to the user. */
	emailed: boolean;
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

export interface Passkey {
	id: string;
	name: string;
	created_at: string;
	last_used_at: string | null;
	synced: boolean;
}

export interface SessionInfo {
	id: string;
	created_at: string;
	last_seen_at: string;
	ip: string;
	user_agent: string;
	site_id: string;
	current: boolean;
}

/** A WebAuthn ceremony: options for the browser plus the server's challenge ID. */
export interface Ceremony {
	challenge_id: string;
	options: unknown;
}

export interface GroupRef {
	id: string;
	name: string;
}

export type AccountStatus = 'pending' | 'active' | 'disabled';

export interface AdminUser extends User {
	status: AccountStatus;
	created_at: string;
	last_login_at: string | null;
	has_totp: boolean;
	passkeys: number;
	recovery_codes: number;
	groups: GroupRef[];
}

export interface Group {
	id: string;
	name: string;
	description: string;
	member_count: number;
	created_at: string;
}

export interface GroupDetail extends Group {
	members: User[];
}

export interface AuditEntry extends AuditEvent {
	actor: string | null;
	subject: string | null;
	details: Record<string, unknown>;
}

export interface Profile {
	display_name: string;
	given_name: string;
	family_name: string;
	locale: 'nl' | 'en';
	/** Absolute URL of the profile picture, or empty. */
	picture: string;
}

export interface DriveNode {
	id: string;
	parent_id: string | null;
	kind: 'folder' | 'file';
	name: string;
	size: number;
	content_type: string;
	created_at: string;
	updated_at: string;
	trashed_at: string | null;
	/** What the signed-in user may do with it. */
	permission: Permission;
	/** Shared with others (only reported to users who may manage it). */
	shared: boolean;
	/** Who last changed it (in folder listings). */
	updated_by: string | null;
	updated_by_name: string;
}

/** Sharing levels; each includes the ones before it. */
export type Permission = 'view' | 'edit' | 'manage';

export interface FolderView {
	folder: DriveNode | null;
	path: { id: string; name: string }[];
	children: DriveNode[];
	/** The folder belongs to someone else and is shared with the user. */
	shared: boolean;
	/** The team folder the folder belongs to, if any. */
	team: TeamRef | null;
}

export interface TeamRef {
	id: string;
	root_id: string;
	name: string;
}

/** A team folder as administrators see it: no contents, only who has access. */
export interface TeamFolder {
	id: string;
	root_id: string;
	name: string;
	groups: Share[];
	usage: Usage;
	created_at: string;
	/** Set while the team folder is deleted (in the trash). */
	deleted_at: string | null;
}

export interface ShareLink {
	id: string;
	/** Only present right after creating the link. */
	token: string;
	password: boolean;
	expires_at: string | null;
	created_at: string;
	downloads: number;
}

/** What a public link shows. */
export interface PublicView {
	node: DriveNode;
	folder: DriveNode | null;
	path: { id: string; name: string }[];
	children: DriveNode[];
}

export interface AppPassword {
	id: string;
	name: string;
	/** Only present right after creating it. */
	password: string;
	created_at: string;
	last_used_at: string | null;
}

export interface SearchResult extends DriveNode {
	/** The folder it is in, or "Mijn bestanden", "Teammappen", "Gedeeld met mij". */
	location: string;
	location_id: string | null;
}

export interface SharedNode extends DriveNode {
	owner_name: string;
}

export interface Subject {
	type: 'user' | 'group';
	id: string;
}

export interface Share {
	subject: Subject;
	name: string;
	permission: Permission;
	granted_at: string;
}

export interface DirectoryEntry extends Subject {
	name: string;
	/** Username, or the group's description. */
	detail: string;
	picture: string;
}

export interface FileVersion {
	id: string;
	size: number;
	content_type: string;
	created_at: string;
	created_by_name: string;
	current: boolean;
}

export interface Usage {
	used_bytes: number;
	limit_bytes: number | null;
}

export interface OidcRequest {
	app_name: string;
	/** The app asked for no sign-in page at all (prompt=none). */
	prompt_none: boolean;
}

export interface OidcClient {
	id: string;
	name: string;
	confidential: boolean;
	redirect_uris: string[];
	post_logout_redirect_uris: string[];
	allowed_group: GroupRef | null;
	backchannel_logout_uri: string;
	created_at: string;
}

export interface OidcClientInput {
	name: string;
	confidential: boolean;
	redirect_uris: string[];
	post_logout_redirect_uris: string[];
	allowed_group_id: string | null;
	backchannel_logout_uri: string;
}

export interface AppGrant {
	client_id: string;
	name: string;
	first_login_at: string;
	last_login_at: string;
}

export interface LogoutRequest {
	app_name: string;
	redirect: string;
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
		is_admin: bool(o, 'is_admin'),
		email: str(o, 'email')
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
	list(record(v, 'activity'), 'events', (e) => parseAuditEvent(e));

const noContent: Validator<void> = () => undefined;

function nullableStr(o: Record<string, unknown>, key: string): string | null {
	return o[key] === null ? null : str(o, key);
}

const parsePasskey: Validator<Passkey> = (v) => {
	const r = record(v, 'passkey');
	return {
		id: str(r, 'id'),
		name: str(r, 'name'),
		created_at: str(r, 'created_at'),
		last_used_at: nullableStr(r, 'last_used_at'),
		synced: bool(r, 'synced')
	};
};

const parsePasskeys: Validator<Passkey[]> = (v) => list(record(v, 'passkeys'), 'passkeys', parsePasskey);

const parseSessions: Validator<SessionInfo[]> = (v) =>
	list(record(v, 'sessions'), 'sessions', (s) => {
		const r = record(s, 'session');
		return {
			id: str(r, 'id'),
			created_at: str(r, 'created_at'),
			last_seen_at: str(r, 'last_seen_at'),
			ip: str(r, 'ip'),
			user_agent: str(r, 'user_agent'),
			site_id: str(r, 'site_id'),
			current: bool(r, 'current')
		};
	});

const parseCeremony: Validator<Ceremony> = (v) => {
	const o = record(v, 'ceremony');
	record(o.options, 'options');
	return { challenge_id: str(o, 'challenge_id'), options: o.options };
};

const parseCodes: Validator<string[]> = (v) => list(record(v, 'codes'), 'codes', (c) => {
	if (typeof c !== 'string') fail('code');
	return c;
});

const parseGroupRef: Validator<GroupRef> = (v) => {
	const r = record(v, 'group');
	return { id: str(r, 'id'), name: str(r, 'name') };
};

const accountStatuses: readonly string[] = ['pending', 'active', 'disabled'];

const parseAdminUser: Validator<AdminUser> = (v) => {
	const r = record(v, 'user');
	const status = str(r, 'status');
	if (!accountStatuses.includes(status)) fail('status');
	return {
		...parseUser(r),
		status: status as AccountStatus,
		created_at: str(r, 'created_at'),
		last_login_at: nullableStr(r, 'last_login_at'),
		has_totp: bool(r, 'has_totp'),
		passkeys: num(r, 'passkeys'),
		recovery_codes: num(r, 'recovery_codes'),
		groups: list(r, 'groups', parseGroupRef)
	};
};

const parseGroup: Validator<Group> = (v) => {
	const r = record(v, 'group');
	return {
		id: str(r, 'id'),
		name: str(r, 'name'),
		description: str(r, 'description'),
		member_count: num(r, 'member_count'),
		created_at: str(r, 'created_at')
	};
};

const parseGroupDetail: Validator<GroupDetail> = (v) => ({
	...parseGroup(v),
	members: list(record(v, 'group'), 'members', parseUser)
});

const parseAuditEvent: Validator<AuditEvent> = (e) => {
	const r = record(e, 'event');
	return {
		id: str(r, 'id'),
		occurred_at: str(r, 'occurred_at'),
		event: str(r, 'event'),
		site_id: str(r, 'site_id'),
		ip: str(r, 'ip'),
		user_agent: str(r, 'user_agent')
	};
};

const parseAuditEntry: Validator<AuditEntry> = (e) => {
	const r = record(e, 'entry');
	return {
		...parseAuditEvent(r),
		actor: nullableStr(r, 'actor'),
		subject: nullableStr(r, 'subject'),
		details: record(r.details, 'details')
	};
};

const parseActivation: Validator<Activation> = (v) => {
	const o = record(v, 'activation');
	const link = str(o, 'activation_link');
	// Shape check: an http(s) activation link, never e.g. a javascript: URL.
	if (!/^https?:\/\/[^\s/]+\/activate#[A-Za-z0-9_-]+$/.test(link)) fail('activation_link');
	return { link, emailed: bool(o, 'emailed') };
};

function strList(o: Record<string, unknown>, key: string): string[] {
	return list(o, key, (v) => {
		if (typeof v !== 'string') fail(key);
		return v;
	});
}

const parseClient: Validator<OidcClient> = (v) => {
	const r = record(v, 'client');
	return {
		id: str(r, 'id'),
		name: str(r, 'name'),
		confidential: bool(r, 'confidential'),
		redirect_uris: strList(r, 'redirect_uris'),
		post_logout_redirect_uris: strList(r, 'post_logout_redirect_uris'),
		allowed_group: r.allowed_group === null ? null : parseGroupRef(r.allowed_group),
		backchannel_logout_uri: str(r, 'backchannel_logout_uri'),
		created_at: str(r, 'created_at')
	};
};

const parseClientWithSecret: Validator<{ client: OidcClient; secret: string }> = (v) => {
	const r = record(v, 'client');
	return { client: parseClient(r), secret: typeof r.secret === 'string' ? r.secret : '' };
};

const parseRedirect: Validator<string> = (v) => {
	const redirect = str(record(v, 'redirect'), 'redirect');
	// Only follow redirects to the provider's own callback.
	if (!/^https?:\/\/[^\s/]+\/oidc\/authorize\/callback\?id=[0-9a-f-]+$/.test(redirect)) fail('redirect');
	return redirect;
};

const parseProfile: Validator<Profile> = (v) => {
	const r = record(v, 'profile');
	const locale = str(r, 'locale');
	if (locale !== 'nl' && locale !== 'en') fail('locale');
	const picture = str(r, 'picture');
	if (picture && !/^https?:\/\/[^\s/]+\/avatars\/[0-9a-f]{32}\.jpg$/.test(picture)) fail('picture');
	return {
		display_name: str(r, 'display_name'),
		given_name: str(r, 'given_name'),
		family_name: str(r, 'family_name'),
		locale,
		picture
	};
};

function permission(o: Record<string, unknown>, key: string): Permission {
	const p = str(o, key);
	if (p !== 'view' && p !== 'edit' && p !== 'manage') fail(key);
	return p;
}

function subjectType(o: Record<string, unknown>): Subject['type'] {
	const t = str(o, 'type');
	if (t !== 'user' && t !== 'group') fail('type');
	return t;
}

const parseNode: Validator<DriveNode> = (v) => {
	const r = record(v, 'node');
	const kind = str(r, 'kind');
	if (kind !== 'folder' && kind !== 'file') fail('kind');
	return {
		id: str(r, 'id'),
		parent_id: r.parent_id === null || r.parent_id === undefined ? null : str(r, 'parent_id'),
		kind,
		name: str(r, 'name'),
		size: num(r, 'size'),
		content_type: str(r, 'content_type'),
		created_at: str(r, 'created_at'),
		updated_at: str(r, 'updated_at'),
		trashed_at: r.trashed_at === undefined || r.trashed_at === null ? null : str(r, 'trashed_at'),
		permission: permission(r, 'permission'),
		shared: r.shared === true,
		updated_by: r.updated_by === null || r.updated_by === undefined ? null : str(r, 'updated_by'),
		updated_by_name: typeof r.updated_by_name === 'string' ? r.updated_by_name : ''
	};
};

const parseLink: Validator<ShareLink> = (v) => {
	const r = record(v, 'link');
	const token = typeof r.token === 'string' ? r.token : '';
	if (token && !/^[A-Za-z0-9_-]{43}$/.test(token)) fail('token');
	return {
		id: str(r, 'id'),
		token,
		password: bool(r, 'password'),
		expires_at: nullableStr(r, 'expires_at'),
		created_at: str(r, 'created_at'),
		downloads: num(r, 'downloads')
	};
};

const parsePublicView: Validator<PublicView> = (v) => {
	const r = record(v, 'link');
	return {
		node: parseNode(r.node),
		folder: r.folder === null || r.folder === undefined ? null : parseNode(r.folder),
		path: list(r, 'path', (c) => {
			const p = record(c, 'crumb');
			return { id: str(p, 'id'), name: str(p, 'name') };
		}),
		children: list(r, 'children', parseNode)
	};
};

const parseAppPassword: Validator<AppPassword> = (v) => {
	const r = record(v, 'app_password');
	const password = typeof r.password === 'string' ? r.password : '';
	if (password && !/^[a-z2-7]{5}(-[a-z2-7]{1,5}){5}$/.test(password)) fail('password');
	return {
		id: str(r, 'id'),
		name: str(r, 'name'),
		password,
		created_at: str(r, 'created_at'),
		last_used_at: nullableStr(r, 'last_used_at')
	};
};

const parseSearchResult: Validator<SearchResult> = (v) => {
	const r = record(v, 'result');
	return { ...parseNode(r), location: str(r, 'location'), location_id: nullableStr(r, 'location_id') };
};

const parseSharedNode: Validator<SharedNode> = (v) => ({
	...parseNode(v),
	owner_name: str(record(v, 'node'), 'owner_name')
});

const parseShare: Validator<Share> = (v) => {
	const r = record(v, 'share');
	const sub = record(r.subject, 'subject');
	return {
		subject: { type: subjectType(sub), id: str(sub, 'id') },
		name: str(r, 'name'),
		permission: permission(r, 'permission'),
		granted_at: str(r, 'granted_at')
	};
};

const parseShares: Validator<Share[]> = (v) => list(record(v, 'shares'), 'shares', parseShare);

const parseDirectoryEntry: Validator<DirectoryEntry> = (v) => {
	const r = record(v, 'entry');
	const picture = str(r, 'picture');
	if (picture && !/^https?:\/\/[^\s/]+\/avatars\/[0-9a-f]{32}\.jpg$/.test(picture)) fail('picture');
	return { type: subjectType(r), id: str(r, 'id'), name: str(r, 'name'), detail: str(r, 'detail'), picture };
};

const parseVersion: Validator<FileVersion> = (v) => {
	const r = record(v, 'version');
	return {
		id: str(r, 'id'),
		size: num(r, 'size'),
		content_type: str(r, 'content_type'),
		created_at: str(r, 'created_at'),
		created_by_name: str(r, 'created_by_name'),
		current: bool(r, 'current')
	};
};

const parseFolderView: Validator<FolderView> = (v) => {
	const r = record(v, 'folder');
	return {
		folder: r.folder === null ? null : parseNode(r.folder),
		path: list(r, 'path', (c) => {
			const p = record(c, 'crumb');
			return { id: str(p, 'id'), name: str(p, 'name') };
		}),
		children: list(r, 'children', parseNode),
		shared: bool(r, 'shared'),
		team: r.team === null || r.team === undefined ? null : parseTeamRef(r.team)
	};
};

const parseTeamRef: Validator<TeamRef> = (v) => {
	const r = record(v, 'team');
	return { id: str(r, 'id'), root_id: str(r, 'root_id'), name: str(r, 'name') };
};

const parseTeamFolder: Validator<TeamFolder> = (v) => {
	const r = record(v, 'team_folder');
	return {
		id: str(r, 'id'),
		root_id: str(r, 'root_id'),
		name: str(r, 'name'),
		groups: list(r, 'groups', parseShare),
		usage: parseUsage(r.usage),
		created_at: str(r, 'created_at'),
		deleted_at: nullableStr(r, 'deleted_at')
	};
};

const parseUsage: Validator<Usage> = (v) => {
	const r = record(v, 'usage');
	return { used_bytes: num(r, 'used_bytes'), limit_bytes: r.limit_bytes === null ? null : num(r, 'limit_bytes') };
};

const parseRemaining: Validator<number> = (v) => num(record(v, 'recovery'), 'remaining');
const parseRevoked: Validator<number> = (v) => num(record(v, 'revoked'), 'revoked');

async function request<T>(
	method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
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

const enc = encodeURIComponent;

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
	loginRecovery: (code: string, f: typeof fetch = fetch) =>
		post('/api/v1/auth/login/recovery', parseStateOnly, { code }, f),
	passkeyLoginBegin: (f: typeof fetch = fetch) => post('/api/v1/auth/passkey/begin', parseCeremony, undefined, f),
	passkeyLoginFinish: (challengeId: string, credential: unknown, f: typeof fetch = fetch) =>
		post('/api/v1/auth/passkey/finish', parseStateOnly, { challenge_id: challengeId, credential }, f),

	passkeys: (f: typeof fetch = fetch) => get('/api/v1/account/passkeys', parsePasskeys, f),
	passkeyRegistrationBegin: (f: typeof fetch = fetch) =>
		post('/api/v1/account/passkeys/registration', parseCeremony, undefined, f),
	passkeyRegistrationFinish: (challengeId: string, name: string, credential: unknown, f: typeof fetch = fetch) =>
		post('/api/v1/account/passkeys/registration/finish', parsePasskey, { challenge_id: challengeId, name, credential }, f),
	deletePasskey: (id: string, f: typeof fetch = fetch) =>
		request('DELETE', `/api/v1/account/passkeys/${encodeURIComponent(id)}`, noContent, undefined, f),
	recoveryCodesRemaining: (f: typeof fetch = fetch) => get('/api/v1/account/recovery-codes', parseRemaining, f),
	generateRecoveryCodes: (f: typeof fetch = fetch) =>
		post('/api/v1/account/recovery-codes', parseCodes, undefined, f),
	sessions: (f: typeof fetch = fetch) => get('/api/v1/account/sessions', parseSessions, f),
	revokeSession: (id: string, f: typeof fetch = fetch) =>
		request('DELETE', `/api/v1/account/sessions/${encodeURIComponent(id)}`, noContent, undefined, f),
	revokeOtherSessions: (f: typeof fetch = fetch) =>
		post('/api/v1/account/sessions/revoke-others', parseRevoked, undefined, f),
	oidcRequest: (id: string, f: typeof fetch = fetch) =>
		get(`/api/v1/oidc/requests/${enc(id)}`, (v) => {
			const r = record(v, 'request');
			return { app_name: str(r, 'app_name'), prompt_none: bool(r, 'prompt_none') } as OidcRequest;
		}, f),
	oidcApprove: (id: string, f: typeof fetch = fetch) =>
		post(`/api/v1/oidc/requests/${enc(id)}/approve`, parseRedirect, undefined, f),
	/** Where to send the browser to cancel a sign-in; the app gets an error. */
	oidcDenyURL: (id: string, reason: 'access_denied' | 'login_required' = 'access_denied') =>
		`/api/v1/oidc/requests/${enc(id)}/deny?error=${reason}`,

	profile: (f: typeof fetch = fetch) => get('/api/v1/account/profile', parseProfile, f),
	updateProfile: (p: Omit<Profile, 'picture'>, f: typeof fetch = fetch) =>
		request('PATCH', '/api/v1/account/profile', parseProfile, p, f),
	/** Uploads a PNG or JPEG as the profile picture; the server crops and re-encodes it. */
	uploadAvatar: async (file: File, f: typeof fetch = fetch): Promise<Profile> => {
		let res: Response;
		try {
			res = await f('/api/v1/account/avatar', {
				method: 'PUT',
				headers: { 'Content-Type': file.type, Accept: 'application/json' },
				body: file,
				credentials: 'same-origin'
			});
		} catch {
			throw new ApiError(0, 'network', 'De server is niet bereikbaar');
		}
		const data: unknown = await res.json().catch(() => null);
		if (!res.ok) {
			const err = (data as { error?: { code?: string; message?: string } } | null)?.error;
			throw new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `Serverfout (${res.status})`);
		}
		return parseProfile(data);
	},
	removeAvatar: (f: typeof fetch = fetch) => request('DELETE', '/api/v1/account/avatar', parseProfile, undefined, f),
	appGrants: (f: typeof fetch = fetch) =>
		get('/api/v1/account/apps', (v) =>
			list(record(v, 'apps'), 'apps', (a) => {
				const r = record(a, 'app');
				return {
					client_id: str(r, 'client_id'),
					name: str(r, 'name'),
					first_login_at: str(r, 'first_login_at'),
					last_login_at: str(r, 'last_login_at')
				} as AppGrant;
			}), f),
	revokeApp: (clientId: string, f: typeof fetch = fetch) =>
		request('DELETE', `/api/v1/account/apps/${enc(clientId)}`, noContent, undefined, f),
	/** Signs out of Cloud and of every app (apps are told via back-channel logout). */
	logoutEverywhere: (f: typeof fetch = fetch) => post('/api/v1/auth/logout-everywhere', noContent, {}, f),
	logoutRequest: (id: string, f: typeof fetch = fetch) =>
		get(`/api/v1/oidc/logout/${enc(id)}`, (v) => {
			const r = record(v, 'logout');
			const redirect = str(r, 'redirect');
			if (!/^https?:\/\//.test(redirect)) fail('redirect');
			return { app_name: str(r, 'app_name'), redirect } as LogoutRequest;
		}, f),
	drive: {
		folder: (id: string | null, f: typeof fetch = fetch) =>
			get(`/api/v1/drive/folders/${id ? enc(id) : 'root'}`, parseFolderView, f),
		createFolder: (parentId: string | null, name: string, f: typeof fetch = fetch) =>
			post('/api/v1/drive/folders', parseNode, { parent_id: parentId ?? 'root', name }, f),
		update: (id: string, change: { name?: string; parent_id?: string }, f: typeof fetch = fetch) =>
			request('PATCH', `/api/v1/drive/nodes/${enc(id)}`, parseNode, change, f),
		trash: (id: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/nodes/${enc(id)}`, noContent, undefined, f),
		listTrash: (f: typeof fetch = fetch) =>
			get('/api/v1/drive/trash', (v) => list(record(v, 'trash'), 'nodes', parseNode), f),
		restore: (id: string, f: typeof fetch = fetch) =>
			post(`/api/v1/drive/trash/${enc(id)}/restore`, parseNode, undefined, f),
		deleteForever: (id: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/trash/${enc(id)}`, noContent, undefined, f),
		emptyTrash: (f: typeof fetch = fetch) => request('DELETE', '/api/v1/drive/trash', noContent, undefined, f),
		usage: (f: typeof fetch = fetch) => get('/api/v1/drive/usage', parseUsage, f),
		downloadURL: (id: string) => `/api/v1/drive/files/${enc(id)}/content`,
		previewURL: (id: string) => `/api/v1/drive/files/${enc(id)}/content?inline=1`,
		teamFolders: (f: typeof fetch = fetch) =>
			get('/api/v1/drive/team-folders', (v) => list(record(v, 'team_folders'), 'nodes', parseNode), f),
		teamTrash: (teamId: string, f: typeof fetch = fetch) =>
			get(`/api/v1/drive/team-folders/${enc(teamId)}/trash`, (v) => {
				const r = record(v, 'trash');
				return { team: parseTeamRef(r.team), nodes: list(r, 'nodes', parseNode) };
			}, f),
		emptyTeamTrash: (teamId: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/team-folders/${enc(teamId)}/trash`, noContent, undefined, f),
		links: (id: string, f: typeof fetch = fetch) =>
			get(`/api/v1/drive/nodes/${enc(id)}/links`, (v) => list(record(v, 'links'), 'links', parseLink), f),
		createLink: (id: string, opts: { expires_at: string | null; password: string }, f: typeof fetch = fetch) =>
			post(`/api/v1/drive/nodes/${enc(id)}/links`, parseLink, opts, f),
		deleteLink: (linkId: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/links/${enc(linkId)}`, noContent, undefined, f),
		/** Opens a public link; no account needed. */
		openLink: (token: string, password: string, folderId: string | null, f: typeof fetch = fetch) =>
			post('/api/v1/drive/public/links/open', parsePublicView, { token, password, folder_id: folderId ?? '' }, f),
		linkDownloadURL: '/api/v1/drive/public/links/download',
		search: (query: string, f: typeof fetch = fetch) =>
			get(`/api/v1/drive/search?q=${enc(query)}`, (v) => list(record(v, 'search'), 'results', parseSearchResult), f),
		sharedWithMe: (f: typeof fetch = fetch) =>
			get('/api/v1/drive/shared', (v) => list(record(v, 'shared'), 'nodes', parseSharedNode), f),
		shares: (id: string, f: typeof fetch = fetch) => get(`/api/v1/drive/nodes/${enc(id)}/shares`, parseShares, f),
		share: (id: string, subject: Subject, permission: Permission, f: typeof fetch = fetch) =>
			request('PUT', `/api/v1/drive/nodes/${enc(id)}/shares`, parseShares, { subject, permission }, f),
		unshare: (id: string, subject: Subject, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/nodes/${enc(id)}/shares/${subject.type}/${enc(subject.id)}`, noContent, undefined, f),
		versions: (id: string, f: typeof fetch = fetch) =>
			get(`/api/v1/drive/files/${enc(id)}/versions`, (v) => list(record(v, 'versions'), 'versions', parseVersion), f),
		versionURL: (id: string, versionId: string) =>
			`/api/v1/drive/files/${enc(id)}/versions/${enc(versionId)}/content`,
		restoreVersion: (id: string, versionId: string, f: typeof fetch = fetch) =>
			post(`/api/v1/drive/files/${enc(id)}/versions/${enc(versionId)}/restore`, parseNode, undefined, f),
		deleteVersion: (id: string, versionId: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/files/${enc(id)}/versions/${enc(versionId)}`, noContent, undefined, f),
		userUsage: (userId: string, f: typeof fetch = fetch) =>
			get(`/api/v1/drive/admin/users/${enc(userId)}/usage`, parseUsage, f),
		setQuota: (userId: string, limitBytes: number | null, f: typeof fetch = fetch) =>
			request('PUT', `/api/v1/drive/admin/users/${enc(userId)}/quota`, parseUsage, { limit_bytes: limitBytes }, f),

		/**
		 * Uploads a file with progress reporting. fetch() cannot report upload
		 * progress, so this uses XMLHttpRequest.
		 */
		upload: (
			parentId: string | null,
			file: File,
			replace: boolean,
			onProgress: (fraction: number) => void
		): Promise<DriveNode> =>
			new Promise((resolve, reject) => {
				const q = new URLSearchParams({ parent: parentId ?? 'root', name: file.name, replace: String(replace) });
				const xhr = new XMLHttpRequest();
				xhr.open('PUT', `/api/v1/drive/files?${q}`);
				xhr.setRequestHeader('Content-Type', 'application/octet-stream');
				xhr.setRequestHeader('Accept', 'application/json');
				xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
				xhr.onerror = () => reject(new ApiError(0, 'network', 'De server is niet bereikbaar'));
				xhr.onload = () => {
					let data: unknown = null;
					try {
						data = JSON.parse(xhr.responseText);
					} catch {
						/* handled below */
					}
					if (xhr.status >= 200 && xhr.status < 300) {
						try {
							resolve(parseNode(data));
						} catch (e) {
							reject(e);
						}
						return;
					}
					const err = (data as { error?: { code?: string; message?: string } } | null)?.error;
					reject(new ApiError(xhr.status, err?.code ?? 'http_error', err?.message ?? `Serverfout (${xhr.status})`));
				};
				xhr.send(file);
			})
	},
	appPasswords: (f: typeof fetch = fetch) =>
		get('/api/v1/account/app-passwords', (v) => list(record(v, 'app_passwords'), 'app_passwords', parseAppPassword), f),
	createAppPassword: (name: string, f: typeof fetch = fetch) =>
		post('/api/v1/account/app-passwords', parseAppPassword, { name }, f),
	revokeAppPassword: (id: string, f: typeof fetch = fetch) =>
		request('DELETE', `/api/v1/account/app-passwords/${enc(id)}`, noContent, undefined, f),
	/** Users and groups to share with. */
	directory: (query: string, f: typeof fetch = fetch) =>
		get(`/api/v1/directory?q=${enc(query)}`, (v) => list(record(v, 'directory'), 'entries', parseDirectoryEntry), f),
	ownGroups: (f: typeof fetch = fetch) =>
		get('/api/v1/account/groups', (v) => list(record(v, 'groups'), 'groups', parseGroupRef), f),

	admin: {
		users: (f: typeof fetch = fetch) =>
			get('/api/v1/admin/users', (v) => list(record(v, 'users'), 'users', parseAdminUser), f),
		user: (id: string, f: typeof fetch = fetch) => get(`/api/v1/admin/users/${enc(id)}`, parseAdminUser, f),
		invite: (
			user: { username: string; display_name: string; is_admin: boolean; email: string },
			f: typeof fetch = fetch
		) => post('/api/v1/admin/users', parseActivation, user, f),
		updateUser: (
			id: string,
			changes: { display_name?: string; is_admin?: boolean; email?: string },
			f: typeof fetch = fetch
		) =>
			request('PATCH', `/api/v1/admin/users/${enc(id)}`, noContent, changes, f),
		resetUser: (id: string, f: typeof fetch = fetch) =>
			post(`/api/v1/admin/users/${enc(id)}/reset`, parseActivation, undefined, f),
		setDisabled: (id: string, disabled: boolean, f: typeof fetch = fetch) =>
			post(`/api/v1/admin/users/${enc(id)}/${disabled ? 'disable' : 'enable'}`, noContent, undefined, f),
		userActivity: (id: string, f: typeof fetch = fetch) =>
			get(`/api/v1/admin/users/${enc(id)}/activity`, parseActivity, f),

		groups: (f: typeof fetch = fetch) =>
			get('/api/v1/admin/groups', (v) => list(record(v, 'groups'), 'groups', parseGroup), f),
		group: (id: string, f: typeof fetch = fetch) => get(`/api/v1/admin/groups/${enc(id)}`, parseGroupDetail, f),
		createGroup: (name: string, description: string, f: typeof fetch = fetch) =>
			post('/api/v1/admin/groups', parseGroup, { name, description }, f),
		updateGroup: (id: string, name: string, description: string, f: typeof fetch = fetch) =>
			request('PATCH', `/api/v1/admin/groups/${enc(id)}`, noContent, { name, description }, f),
		deleteGroup: (id: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/admin/groups/${enc(id)}`, noContent, undefined, f),
		setMember: (groupId: string, userId: string, member: boolean, f: typeof fetch = fetch) =>
			request(member ? 'PUT' : 'DELETE', `/api/v1/admin/groups/${enc(groupId)}/members/${enc(userId)}`, noContent, undefined, f),

		apps: (f: typeof fetch = fetch) =>
			get('/api/v1/admin/apps', (v) => {
				const r = record(v, 'apps');
				return { issuer: str(r, 'issuer'), clients: list(r, 'clients', parseClient) };
			}, f),
		app: (id: string, f: typeof fetch = fetch) => get(`/api/v1/admin/apps/${enc(id)}`, parseClient, f),
		createApp: (input: OidcClientInput, f: typeof fetch = fetch) =>
			post('/api/v1/admin/apps', parseClientWithSecret, input, f),
		updateApp: (id: string, input: OidcClientInput, f: typeof fetch = fetch) =>
			request('PATCH', `/api/v1/admin/apps/${enc(id)}`, noContent, input, f),
		rotateAppSecret: (id: string, f: typeof fetch = fetch) =>
			post(`/api/v1/admin/apps/${enc(id)}/secret`, (v) => str(record(v, 'secret'), 'secret'), undefined, f),
		deleteApp: (id: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/admin/apps/${enc(id)}`, noContent, undefined, f),

		teamFolders: (f: typeof fetch = fetch) =>
			get('/api/v1/drive/admin/team-folders/', (v) => list(record(v, 'team_folders'), 'team_folders', parseTeamFolder), f),
		createTeamFolder: (name: string, f: typeof fetch = fetch) =>
			post('/api/v1/drive/admin/team-folders/', parseTeamFolder, { name }, f),
		renameTeamFolder: (id: string, name: string, f: typeof fetch = fetch) =>
			request('PATCH', `/api/v1/drive/admin/team-folders/${enc(id)}`, parseTeamFolder, { name }, f),
		setTeamFolderQuota: (id: string, limitBytes: number | null, f: typeof fetch = fetch) =>
			request('PUT', `/api/v1/drive/admin/team-folders/${enc(id)}/quota`, parseTeamFolder, { limit_bytes: limitBytes }, f),
		setTeamFolderGroup: (id: string, groupId: string, permission: Permission, f: typeof fetch = fetch) =>
			request('PUT', `/api/v1/drive/admin/team-folders/${enc(id)}/groups/${enc(groupId)}`, parseTeamFolder, { permission }, f),
		removeTeamFolderGroup: (id: string, groupId: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/admin/team-folders/${enc(id)}/groups/${enc(groupId)}`, parseTeamFolder, undefined, f),
		deleteTeamFolder: (id: string, f: typeof fetch = fetch) =>
			request('DELETE', `/api/v1/drive/admin/team-folders/${enc(id)}`, parseTeamFolder, undefined, f),
		restoreTeamFolder: (id: string, f: typeof fetch = fetch) =>
			post(`/api/v1/drive/admin/team-folders/${enc(id)}/restore`, parseTeamFolder, undefined, f),
		purgeTeamFolder: (id: string, f: typeof fetch = fetch) =>
			post(`/api/v1/drive/admin/team-folders/${enc(id)}/purge`, noContent, undefined, f),

		audit: (before: string | null, f: typeof fetch = fetch) =>
			get(
				`/api/v1/admin/audit${before ? `?before=${enc(before)}` : ''}`,
				(v) => list(record(v, 'audit'), 'entries', parseAuditEntry),
				f
			)
	},

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
