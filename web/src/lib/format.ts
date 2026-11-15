const relative = new Intl.RelativeTimeFormat('nl', { numeric: 'auto' });

const steps: [limit: number, unit: Intl.RelativeTimeFormatUnit, seconds: number][] = [
	[60, 'second', 1],
	[3600, 'minute', 60],
	[86400, 'hour', 3600],
	[Infinity, 'day', 86400]
];

/** Formats an ISO timestamp relative to now, e.g. "2 minuten geleden". */
export function timeAgo(iso: string, now: Date = new Date()): string {
	const then = new Date(iso);
	if (Number.isNaN(then.getTime())) return 'onbekend';

	const diff = Math.round((then.getTime() - now.getTime()) / 1000);
	if (Math.abs(diff) < 10) return 'zojuist';
	for (const [limit, unit, seconds] of steps) {
		if (Math.abs(diff) < limit) return relative.format(Math.round(diff / seconds), unit);
	}
	return 'onbekend';
}

/** Formats an ISO timestamp as a full local date and time, for tooltips. */
export function dateTime(iso: string): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return 'onbekend';
	return d.toLocaleString('nl-NL', { dateStyle: 'medium', timeStyle: 'medium' });
}

const checkLabels: Record<string, string> = {
	database: 'Database',
	storage: 'Objectopslag (S3)'
};

/** Returns the Dutch display label for a health check name. */
export function checkLabel(name: string): string {
	return checkLabels[name] ?? name;
}

const eventLabels: Record<string, string> = {
	'user.created': 'Account aangemaakt',
	'auth.activation.issued': 'Activatielink uitgegeven',
	'auth.activation.started': 'Wachtwoord ingesteld via activatielink',
	'auth.totp.enrolled': 'Authenticator-app gekoppeld',
	'auth.login.succeeded': 'Ingelogd',
	'auth.login.failed': 'Mislukte inlogpoging',
	'auth.totp.failed': 'Onjuiste verificatiecode',
	'auth.logout': 'Uitgelogd',
	'auth.password.changed': 'Wachtwoord gewijzigd',
	'auth.passkey.added': 'Passkey toegevoegd',
	'auth.passkey.removed': 'Passkey verwijderd',
	'auth.passkey.failed': 'Mislukte inlogpoging met passkey',
	'auth.recovery_codes.generated': 'Nieuwe herstelcodes aangemaakt',
	'auth.recovery_code.used': 'Herstelcode gebruikt',
	'auth.session.revoked': 'Uitgelogd op ander apparaat',
	'user.updated': 'Account gewijzigd door beheerder',
	'user.disabled': 'Account uitgeschakeld',
	'user.enabled': 'Account ingeschakeld',
	'group.created': 'Groep aangemaakt',
	'group.updated': 'Groep gewijzigd',
	'group.deleted': 'Groep verwijderd',
	'group.member_added': 'Toegevoegd aan groep',
	'group.member_removed': 'Verwijderd uit groep',
	'oidc.login': 'Ingelogd bij een app',
	'oidc.denied': 'Toegang tot app geweigerd',
	'oidc.client.created': 'App geregistreerd',
	'oidc.client.updated': 'App gewijzigd',
	'oidc.client.deleted': 'App verwijderd',
	'oidc.client.secret_rotated': 'Client-secret van app vernieuwd',
	'oidc.app_revoked': 'Toegang van app ingetrokken',
	'auth.logout_everywhere': 'Overal uitgelogd, ook bij apps',
	'user.profile_updated': 'Profiel gewijzigd',
	'share.granted': 'Gedeeld',
	'share.revoked': 'Delen gestopt',
	'teamfolder.created': 'Teammap aangemaakt',
	'teamfolder.renamed': 'Teammap hernoemd',
	'teamfolder.quota_changed': 'Quotum van teammap gewijzigd',
	'teamfolder.deleted': 'Teammap opgeheven',
	'teamfolder.restored': 'Teammap teruggezet',
	'teamfolder.purged': 'Teammap definitief verwijderd'
};

/** Returns the Dutch description of an audit event. */
export function eventLabel(event: string): string {
	return eventLabels[event] ?? event;
}

/** Events that may indicate an attack and deserve attention. */
export function isWarningEvent(event: string): boolean {
	return [
		'auth.login.failed',
		'auth.totp.failed',
		'auth.passkey.failed',
		'auth.recovery_code.used',
		'user.disabled',
		'oidc.denied'
	].includes(event);
}

/**
 * Returns `next` if it is a path on this site, otherwise "/". This prevents an
 * open redirect: a crafted login link must not send the user to another site
 * after signing in.
 */
export function safeNext(next: string | null, origin: string): string {
	if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/';
	try {
		const url = new URL(next, origin);
		if (url.origin !== origin) return '/';
		return url.pathname + url.search + url.hash;
	} catch {
		return '/';
	}
}

/** Groups a base32 secret in blocks of four for easier typing. */
export function groupSecret(secret: string): string {
	return secret.match(/.{1,4}/g)?.join(' ') ?? secret;
}

/** A short description such as "Firefox op Linux" from a User-Agent header. */
export function describeUserAgent(ua: string): string {
	if (ua === 'cli') return 'Opdrachtregel';
	const browser =
		[
			[/Edg\//, 'Edge'],
			[/Firefox\//, 'Firefox'],
			[/Chrome\//, 'Chrome'],
			[/Safari\//, 'Safari']
		].find(([re]) => (re as RegExp).test(ua))?.[1] ?? 'Onbekende browser';
	const os =
		[
			[/Android/, 'Android'],
			[/iPhone|iPad/, 'iOS'],
			[/Mac OS X/, 'macOS'],
			[/Windows/, 'Windows'],
			[/Linux/, 'Linux']
		].find(([re]) => (re as RegExp).test(ua))?.[1];
	return os ? `${browser} op ${os}` : String(browser);
}

const byteUnits = ['B', 'KB', 'MB', 'GB', 'TB'];

/** Formats a byte count for people: 1536 → "1,5 KB" (powers of 1024). */
export function formatBytes(bytes: number): string {
	let value = bytes;
	let unit = 0;
	while (value >= 1024 && unit < byteUnits.length - 1) {
		value /= 1024;
		unit++;
	}
	const digits = unit === 0 || value >= 10 ? 0 : 1;
	return `${value.toLocaleString('nl-NL', { maximumFractionDigits: digits })} ${byteUnits[unit]}`;
}

/** A name that does not clash with existing ones: "foto.jpg" → "foto (2).jpg". */
export function uniqueName(name: string, taken: Set<string>): string {
	const lower = new Set([...taken].map((n) => n.toLowerCase()));
	if (!lower.has(name.toLowerCase())) return name;
	const dot = name.lastIndexOf('.');
	const [base, ext] = dot > 0 ? [name.slice(0, dot), name.slice(dot)] : [name, ''];
	for (let i = 2; ; i++) {
		const candidate = `${base} (${i})${ext}`;
		if (!lower.has(candidate.toLowerCase())) return candidate;
	}
}

const permissionLabels = { view: 'Kan bekijken', edit: 'Kan bewerken', manage: 'Kan beheren' } as const;

/** Returns the Dutch description of a sharing level. */
export function permissionLabel(p: keyof typeof permissionLabels): string {
	return permissionLabels[p];
}

const rank = { view: 1, edit: 2, manage: 3 } as const;

/** Whether permission p includes need (view < edit < manage). */
export function allows(p: keyof typeof rank, need: keyof typeof rank): boolean {
	return rank[p] >= rank[need];
}
