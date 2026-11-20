import { describe, expect, it } from 'vitest';
import type { SessionUser } from './api.ts';
import { can, canAnyIn } from './permissions.ts';

const user = (permissions: string[]): SessionUser => ({ id: 'u', username: 'ann', display_name: 'Ann', email: '', permissions });

describe('permissions', () => {
	const desk = user(['identity.users.view', 'drive.teamfolders.create']);

	it('checks one permission', () => {
		expect(can(desk, 'identity.users.view')).toBe(true);
		expect(can(desk, 'identity.users.reset')).toBe(false);
		expect(can(null, 'identity.users.view')).toBe(false);
	});

	it('checks a module by its prefix only', () => {
		expect(canAnyIn(desk, 'drive')).toBe(true);
		expect(canAnyIn(desk, 'system')).toBe(false);
		expect(canAnyIn(user(['identityx.users.view']), 'identity')).toBe(false);
	});
});
