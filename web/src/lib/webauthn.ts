// Bridges the JSON the server sends (binary fields as base64url) and the
// browser's WebAuthn API (binary fields as ArrayBuffers), in both directions.

export function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
	const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4);
	const bin = atob(b64);
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out;
}

export function bytesToB64url(buf: ArrayBuffer | Uint8Array): string {
	const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
	let bin = '';
	for (const b of bytes) bin += String.fromCharCode(b);
	return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

type Json = Record<string, unknown>;

function publicKeyOf(options: unknown): Json {
	const pk = (options as Json | null)?.publicKey;
	if (typeof pk !== 'object' || pk === null || typeof (pk as Json).challenge !== 'string') {
		throw new Error('Ongeldige passkey-opties van de server');
	}
	return pk as Json;
}

function convertDescriptors(list: unknown): PublicKeyCredentialDescriptor[] | undefined {
	if (!Array.isArray(list)) return undefined;
	return list.map((d: Json) => ({
		type: 'public-key',
		id: b64urlToBytes(String(d.id)),
		transports: Array.isArray(d.transports) ? (d.transports as AuthenticatorTransport[]) : undefined
	}));
}

/** Converts server registration options for navigator.credentials.create(). */
export function toCreationOptions(options: unknown): PublicKeyCredentialCreationOptions {
	const pk = publicKeyOf(options);
	const user = pk.user as Json;
	return {
		...(pk as unknown as PublicKeyCredentialCreationOptions),
		challenge: b64urlToBytes(String(pk.challenge)),
		user: { ...(user as unknown as PublicKeyCredentialUserEntity), id: b64urlToBytes(String(user.id)) },
		excludeCredentials: convertDescriptors(pk.excludeCredentials)
	};
}

/** Converts server login options for navigator.credentials.get(). */
export function toRequestOptions(options: unknown): PublicKeyCredentialRequestOptions {
	const pk = publicKeyOf(options);
	return {
		...(pk as unknown as PublicKeyCredentialRequestOptions),
		challenge: b64urlToBytes(String(pk.challenge)),
		allowCredentials: convertDescriptors(pk.allowCredentials)
	};
}

function common(cred: PublicKeyCredential): Json {
	return {
		id: cred.id,
		rawId: bytesToB64url(cred.rawId),
		type: cred.type,
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
		clientExtensionResults: cred.getClientExtensionResults()
	};
}

export function serializeAttestation(cred: PublicKeyCredential): Json {
	const r = cred.response as AuthenticatorAttestationResponse;
	return {
		...common(cred),
		response: {
			clientDataJSON: bytesToB64url(r.clientDataJSON),
			attestationObject: bytesToB64url(r.attestationObject),
			transports: r.getTransports?.() ?? []
		}
	};
}

export function serializeAssertion(cred: PublicKeyCredential): Json {
	const r = cred.response as AuthenticatorAssertionResponse;
	return {
		...common(cred),
		response: {
			clientDataJSON: bytesToB64url(r.clientDataJSON),
			authenticatorData: bytesToB64url(r.authenticatorData),
			signature: bytesToB64url(r.signature),
			userHandle: r.userHandle ? bytesToB64url(r.userHandle) : undefined
		}
	};
}

export function passkeysSupported(): boolean {
	return typeof window !== 'undefined' && 'PublicKeyCredential' in window;
}

/** A user-facing message for errors thrown by the WebAuthn API. */
export function webauthnErrorMessage(e: unknown): string | null {
	if (!(e instanceof DOMException)) return null;
	switch (e.name) {
		case 'NotAllowedError':
			return 'Geannuleerd of verlopen. Probeer het opnieuw.';
		case 'InvalidStateError':
			return 'Deze passkey is al aan je account gekoppeld.';
		case 'SecurityError':
			return 'Passkeys werken alleen via het juiste adres van deze site (geen IP-adres).';
		default:
			return 'Je browser of apparaat kon de passkey niet gebruiken.';
	}
}

export async function createPasskey(options: unknown): Promise<Json> {
	const cred = (await navigator.credentials.create({ publicKey: toCreationOptions(options) })) as PublicKeyCredential | null;
	if (!cred) throw new DOMException('No credential', 'NotAllowedError');
	return serializeAttestation(cred);
}

export async function getPasskey(options: unknown): Promise<Json> {
	const cred = (await navigator.credentials.get({ publicKey: toRequestOptions(options) })) as PublicKeyCredential | null;
	if (!cred) throw new DOMException('No credential', 'NotAllowedError');
	return serializeAssertion(cred);
}
