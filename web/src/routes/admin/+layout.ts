import { redirect } from '@sveltejs/kit';
import { administers } from '#lib/permissions.ts';
import type { LayoutLoad } from './$types';

// Hides the admin area from users without administrative permissions. The
// API checks every request itself; this only keeps the UI tidy.
export const load: LayoutLoad = async ({ parent }) => {
	const { user } = await parent();
	if (!administers(user)) redirect(307, '/');
};
