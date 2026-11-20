// What the signed-in user may administer (ADR 0022). Only for showing or
// hiding parts of the pages: the API checks every request itself.
import type { AdminPermission, SessionUser } from './api.ts';

/** True when the user has this permission. */
export function can(user: SessionUser | null | undefined, permission: AdminPermission): boolean {
	return !!user?.permissions.includes(permission);
}

/** True when the user has any permission of this module, e.g. "identity" or "drive". */
export function canAnyIn(user: SessionUser | null | undefined, module: string): boolean {
	return !!user?.permissions.some((p) => p.startsWith(`${module}.`));
}
