// State shared by the files sidebar and the page next to it: storage use,
// the user's team folders, which section is open, and what "Nieuw" can do
// in the folder that is shown.
import { api, errorMessage, type DriveNode, type Usage } from './api.ts';

export type Section = 'mine' | 'shared' | 'trash' | { team: string };

export const drive = $state<{
	usage: Usage | null;
	teams: DriveNode[];
	error: string | null;
	section: Section;
	/** Set by the file browser when the user may add to the open folder. */
	create: { folder: () => void; upload: () => void } | null;
}>({ usage: null, teams: [], error: null, section: 'mine', create: null });

export async function refreshDrive(): Promise<void> {
	try {
		[drive.usage, drive.teams] = await Promise.all([api.drive.usage(), api.drive.teamFolders()]);
		drive.error = null;
	} catch (e) {
		drive.error = errorMessage(e);
	}
}
