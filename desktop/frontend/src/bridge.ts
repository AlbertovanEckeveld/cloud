// Typed access to the Go side. Wails exposes bound methods on window.go and
// its runtime (events) on window.runtime.

export interface Report {
	Uploaded: number;
	Downloaded: number;
	FoldersCreated: number;
	DeletedLocal: number;
	TrashedRemote: number;
	Conflicts: number;
	Failed: string[] | null;
}

export interface FolderStatus {
	root: string;
	name: string;
	dir: string;
	sidebar: boolean;
	last_run: string;
	report: Report;
	error: string;
	mass_delete: { local: number; remote: number; total: number } | null;
}

export interface Transfer {
	op: 'upload' | 'download';
	path: string;
	done: number;
	total: number;
	step: number;
	steps: number;
	folder: string;
	started: string;
}

export interface RecentItem {
	at: string;
	op: 'uploaded' | 'downloaded' | 'folder' | 'deleted_local' | 'trashed_remote' | 'conflict';
	path: string;
	folder: string;
}

export interface Status {
	server: string;
	signed_in: boolean;
	syncing: boolean;
	paused: boolean;
	autostart: boolean;
	sidebar_supported: boolean;
	folders: FolderStatus[];
	error: string;
	current: Transfer | null;
	recent: RecentItem[] | null;
}

export interface Root {
	id: string;
	name: string;
	kind: 'mine' | 'team' | 'shared';
	permission: string;
}

interface BridgeAPI {
	Status(): Promise<Status>;
	SignIn(server: string, allowInsecure: boolean): Promise<void>;
	SignOut(): Promise<void>;
	Roots(): Promise<Root[]>;
	ChooseAndAdd(root: string, name: string): Promise<void>;
	RemoveFolder(root: string): Promise<void>;
	SyncNow(): Promise<void>;
	SetPaused(on: boolean): Promise<void>;
	ConfirmMassDelete(root: string): Promise<void>;
	SetSidebar(root: string, on: boolean): Promise<void>;
	SetAutostart(on: boolean): Promise<void>;
	OpenFolder(dir: string): Promise<void>;
}

declare global {
	interface Window {
		go: { main: { Bridge: BridgeAPI } };
	}
}

export const bridge = (): BridgeAPI => window.go.main.Bridge;

/** The status in words, the same as in the tray menu. */
export function summary(s: Status): string {
	if (!s.signed_in) return 'Niet gekoppeld';
	if (s.error) return s.error;
	if (s.folders.some((f) => f.mass_delete)) return 'Wacht op je bevestiging (veel verwijderingen)';
	if (s.paused) return 'Gepauzeerd';
	if (s.syncing) return 'Bezig met synchroniseren…';
	if (s.folders.some((f) => f.error)) return 'Problemen bij synchroniseren';
	if (s.folders.length === 0) return 'Nog geen mappen gekozen';
	return 'Alles bijgewerkt';
}

export function message(e: unknown): string {
	return typeof e === 'string' ? e : e instanceof Error ? e.message : 'Er ging iets mis.';
}
