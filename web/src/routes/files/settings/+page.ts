import { redirect } from '@sveltejs/kit';
import { driveSettingsTabs } from '#lib/drive-settings-tabs.ts';
import type { PageLoad } from './$types';

// The gear opens the first tab this user may see.
export const load: PageLoad = async ({ parent }) => {
	const { user } = await parent();
	redirect(307, driveSettingsTabs(user)[0]?.href ?? '/files');
};
