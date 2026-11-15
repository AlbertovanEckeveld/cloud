import { describe, expect, it } from 'vitest';
import { b64urlToBytes, bytesToB64url, toCreationOptions, toRequestOptions } from './webauthn.ts';

describe('base64url', () => {
	it('round-trips every byte value', () => {
		const all = new Uint8Array(256).map((_, i) => i);
		expect(b64urlToBytes(bytesToB64url(all))).toEqual(all);
	});

	it('uses the URL-safe alphabet without padding', () => {
		expect(bytesToB64url(new Uint8Array([0xfb, 0xff]))).toBe('-_8');
		expect(b64urlToBytes('-_8')).toEqual(new Uint8Array([0xfb, 0xff]));
	});
});

describe('option conversion', () => {
	it('converts registration options to binary fields', () => {
		const opts = toCreationOptions({
			publicKey: {
				challenge: 'AQID',
				rp: { id: 'localhost', name: 'Cloud' },
				user: { id: 'BAU', name: 'alice', displayName: 'Alice' },
				excludeCredentials: [{ type: 'public-key', id: 'Bgc' }],
				pubKeyCredParams: [{ type: 'public-key', alg: -7 }]
			}
		});
		expect(opts.challenge).toEqual(new Uint8Array([1, 2, 3]));
		expect(opts.user.id).toEqual(new Uint8Array([4, 5]));
		expect(opts.user.name).toBe('alice');
		expect(opts.excludeCredentials?.[0].id).toEqual(new Uint8Array([6, 7]));
		expect(opts.rp.id).toBe('localhost');
	});

	it('converts login options', () => {
		const opts = toRequestOptions({ publicKey: { challenge: 'AQID', userVerification: 'required' } });
		expect(opts.challenge).toEqual(new Uint8Array([1, 2, 3]));
		expect(opts.userVerification).toBe('required');
		expect(opts.allowCredentials).toBeUndefined();
	});

	it('rejects options without a challenge', () => {
		expect(() => toRequestOptions({ publicKey: {} })).toThrow();
		expect(() => toRequestOptions(null)).toThrow();
	});
});
