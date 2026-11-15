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
