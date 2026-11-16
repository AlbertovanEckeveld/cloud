// State shared by the files sidebar and the page next to it: storage use,
// the user's team folders, which section is open, and what "Nieuw" can do
// in the folder that is shown.
import { api, errorMessage, type DriveNode, type OfficeInfo, type Usage } from './api.ts';

export type Section = 'mine' | 'shared' | 'trash' | { team: string };

export const drive = $state<{
	usage: Usage | null;
	teams: DriveNode[];
	error: string | null;
	section: Section;
	/** Set by the file browser when the user may add to the open folder. */
	create: { folder: () => void; upload: () => void } | null;
	/** Which documents Collabora can open; null until known. */
	office: OfficeInfo | null;
}>({ usage: null, teams: [], error: null, section: 'mine', create: null, office: null });

/** Whether a file can be opened in the document editor. */
export function editable(name: string): boolean {
	const ext = name.includes('.') ? name.split('.').pop()!.toLowerCase() : '';
	return !!drive.office?.enabled && drive.office.extensions.includes(ext);
}

export async function refreshDrive(): Promise<void> {
	try {
		[drive.usage, drive.teams, drive.office] = await Promise.all([
			api.drive.usage(),
			api.drive.teamFolders(),
			drive.office ?? api.drive.office()
		]);
		drive.error = null;
	} catch (e) {
		drive.error = errorMessage(e);
	}
}
