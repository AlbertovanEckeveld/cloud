import { redirect } from '@sveltejs/kit';
import type { LayoutLoad } from './$types';

// Hides the admin area from non-administrators. The API enforces this
// independently; this only keeps the UI tidy.
export const load: LayoutLoad = async ({ parent }) => {
	const { user } = await parent();
	if (!user?.is_admin) redirect(307, '/');
};
