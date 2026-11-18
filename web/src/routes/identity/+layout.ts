import { redirect } from '@sveltejs/kit';
import { identityTabs } from '#lib/identity-tabs.ts';
import type { LayoutLoad } from './$types';

// Only users with an identity permission get here. The API checks every
// request itself; this only keeps the pages tidy.
export const load: LayoutLoad = async ({ parent }) => {
	const { user } = await parent();
	if (identityTabs(user).length === 0) redirect(307, '/');
};
