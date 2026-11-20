import { redirect } from '@sveltejs/kit';
import { driveSettingsTabs } from '#lib/drive-settings-tabs.ts';
import type { LayoutLoad } from './$types';

// Only for users with a drive permission; the API checks every request too.
export const load: LayoutLoad = async ({ parent }) => {
	const { user } = await parent();
	if (driveSettingsTabs(user).length === 0) redirect(307, '/files');
};
