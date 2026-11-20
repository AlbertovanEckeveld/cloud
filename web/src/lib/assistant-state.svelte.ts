// The assistant panel that every page can open (ADR 0024): whether it is
// open, which conversation it shows, and what the next question is about.
// Focus items are references only; the server reads them after checking
// the user may see them.

export interface Focus {
	source: string;
	id: string;
	title: string;
	kind: string;
}

export const assistant = $state({
	/** Whether a chat model is configured; null until known. */
	available: null as boolean | null,
	open: false,
	conversation: null as string | null,
	focus: [] as Focus[],
	/** A question to send as soon as the panel is ready, then cleared. */
	ask: null as string | null
});

/**
 * Opens the panel in a new conversation about the given items, and sends
 * question right away when one is given (nothing is sent otherwise).
 */
export function askAssistant(question: string | null, focus: Focus[] = []) {
	assistant.conversation = null;
	assistant.focus = focus;
	assistant.ask = question;
	assistant.open = true;
}

export function toggleAssistant() {
	assistant.open = !assistant.open;
}

/** Learns once whether the assistant can answer, to show its buttons. */
export async function loadAssistantStatus(status: () => Promise<{ chat: boolean }>) {
	if (assistant.available !== null) return;
	try {
		assistant.available = (await status()).chat;
	} catch {
		assistant.available = false;
	}
}
