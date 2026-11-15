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
	'auth.session.revoked': 'Uitgelogd op ander apparaat'
};

/** Returns the Dutch description of an audit event. */
export function eventLabel(event: string): string {
	return eventLabels[event] ?? event;
}

/** Events that may indicate an attack and deserve attention. */
export function isWarningEvent(event: string): boolean {
	return ['auth.login.failed', 'auth.totp.failed', 'auth.passkey.failed', 'auth.recovery_code.used'].includes(event);
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
