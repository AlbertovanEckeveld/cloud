// The tabs of the Identity page, each with the permission that shows it.
import type { SessionUser } from './api.ts';
import { can } from './permissions.ts';

const all = [
	{ href: '/identity/users', label: 'Gebruikers', permission: 'identity.users.view' },
	{ href: '/identity/groups', label: 'Groepen', permission: 'identity.groups.view' },
	{ href: '/identity/roles', label: 'Rollen', permission: 'identity.roles.view' },
	{ href: '/identity/apps', label: 'Apps', permission: 'identity.apps.manage' },
	{ href: '/identity/audit', label: 'Audit-log', permission: 'identity.audit.view' }
];

/** The tabs this user may open, in order. */
export function identityTabs(user: SessionUser | null | undefined) {
	return all.filter((t) => can(user, t.permission));
}
