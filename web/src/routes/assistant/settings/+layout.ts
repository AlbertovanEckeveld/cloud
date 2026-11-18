import { redirect } from '@sveltejs/kit';
import { can } from '#lib/permissions.ts';
import type { LayoutLoad } from './$types';

// Only for users who may choose where content goes; the API checks it too.
export const load: LayoutLoad = async ({ parent }) => {
	const { user } = await parent();
	if (!can(user, 'assistant.provider.manage')) redirect(307, '/assistant');
};
