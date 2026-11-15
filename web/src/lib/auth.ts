import { goto, invalidateAll } from '$app/navigation';
import { api, ApiError } from './api.ts';

/** True when the server wants a fresh sign-in before a sensitive change. */
export function isReauthError(e: unknown): boolean {
	return e instanceof ApiError && e.code === 'reauth_required';
}

/** Signs out and returns to the login page, coming back to `next` afterwards. */
export async function reauthenticate(next: string): Promise<void> {
	try {
		await api.logout();
	} finally {
		await invalidateAll();
		await goto(`/login?next=${encodeURIComponent(next)}`);
	}
}
