import { redirect } from '@sveltejs/kit';
import { api } from '#lib/api.ts';
import type { LayoutLoad } from './$types';

// The frontend is a single-page app served by the Go binary: no server-side
// rendering, everything renders in the browser and talks to /api.
export const ssr = false;

/** Pages only for visitors who are not signed in. */
const publicPaths = ['/login', '/activate'];
/** Pages for everyone: signing in to an app, and public share links (/s). */
const openPaths = ['/connect', '/signed-out', '/s'];

export const load: LayoutLoad = async ({ fetch, url }) => {
	const session = await api.session(fetch);
	const authenticated = session?.state === 'authenticated';
	const isPublic = publicPaths.includes(url.pathname);

	if (!authenticated && !isPublic && !openPaths.includes(url.pathname)) {
		const next = url.pathname === '/' ? '' : `?next=${encodeURIComponent(url.pathname + url.search)}`;
		redirect(307, `/login${next}`);
	}
	if (authenticated && isPublic) redirect(307, '/');

	// The API enforces authorization; this only decides what the UI shows.
	return { session, user: authenticated ? session.user : null };
};
