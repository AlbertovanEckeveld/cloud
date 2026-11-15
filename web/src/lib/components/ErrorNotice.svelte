<script lang="ts">
	// Shows an API error. When the server asks for a fresh sign-in, offers it
	// directly and brings the user back to this page afterwards.
	import { page } from '$app/state';
	import { errorMessage } from '#lib/api.ts';
	import { isReauthError, reauthenticate } from '#lib/auth.ts';
	import { webauthnErrorMessage } from '#lib/webauthn.ts';

	let { error }: { error: unknown } = $props();
</script>

{#if error}
	<div class="flash flash--error" role="alert">
		{webauthnErrorMessage(error) ?? errorMessage(error)}
		{#if isReauthError(error)}
			<button class="button" onclick={() => reauthenticate(page.url.pathname)}>Opnieuw inloggen</button>
		{/if}
	</div>
{/if}

<style>
	.flash {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}
</style>
