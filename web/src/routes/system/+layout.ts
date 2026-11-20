import { redirect } from '@sveltejs/kit';
import { can } from '#lib/permissions.ts';
import type { LayoutLoad } from './$types';

// Only for users who may see the system status; the API checks it too.
export const load: LayoutLoad = async ({ parent }) => {
	const { user } = await parent();
	if (!can(user, 'system.status.view')) redirect(307, '/');
};
