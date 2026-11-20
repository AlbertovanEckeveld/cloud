import { redirect } from '@sveltejs/kit';
import { identityTabs } from '#lib/identity-tabs.ts';
import type { PageLoad } from './$types';

// /identity opens the first tab this user may see.
export const load: PageLoad = async ({ parent }) => {
	const { user } = await parent();
	redirect(307, identityTabs(user)[0]?.href ?? '/');
};
