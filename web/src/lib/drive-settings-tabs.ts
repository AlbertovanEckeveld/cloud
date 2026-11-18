// The tabs behind the gear on the files page, each with the permission
// that shows it.
import type { SessionUser } from './api.ts';
import { can } from './permissions.ts';

const all = [
	{ href: '/files/settings/team-folders', label: 'Teammappen', permission: 'drive.teamfolders.view' },
	{ href: '/files/settings/storage', label: 'Opslag per gebruiker', permission: 'drive.quota.manage' }
];

/** The tabs this user may open, in order. */
export function driveSettingsTabs(user: SessionUser | null | undefined) {
	return all.filter((t) => can(user, t.permission));
}
