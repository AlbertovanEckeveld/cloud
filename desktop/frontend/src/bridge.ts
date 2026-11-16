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
	last_run: string;
	report: Report;
	error: string;
	mass_delete: { local: number; remote: number; total: number } | null;
}

export interface Status {
	server: string;
	signed_in: boolean;
	syncing: boolean;
	autostart: boolean;
	folders: FolderStatus[];
	error: string;
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
	ConfirmMassDelete(root: string): Promise<void>;
	SetAutostart(on: boolean): Promise<void>;
	OpenFolder(dir: string): Promise<void>;
	Quit(): Promise<void>;
}

declare global {
	interface Window {
		go: { main: { Bridge: BridgeAPI } };
		runtime: { EventsOn(name: string, cb: (data: unknown) => void): () => void };
	}
}

export const bridge = (): BridgeAPI => window.go.main.Bridge;

export function onStatus(cb: (s: Status) => void): () => void {
	return window.runtime.EventsOn('status', (data) => cb(data as Status));
}

export function message(e: unknown): string {
	return typeof e === 'string' ? e : e instanceof Error ? e.message : 'Er ging iets mis.';
}
