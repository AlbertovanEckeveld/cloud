<script lang="ts">
	// Input for a 6-digit code from an authenticator app. autocomplete
	// "one-time-code" lets phones offer the code, inputmode shows a number pad.
	let { value = $bindable(''), label = 'Code uit je authenticator-app' } = $props();

	function onInput(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		value = input.value.replace(/\D/g, '').slice(0, 6);
		input.value = value;
	}
</script>

<label class="field">
	<span>{label}</span>
	<input
		class="code"
		{value}
		oninput={onInput}
		inputmode="numeric"
		autocomplete="one-time-code"
		pattern={'[0-9]{6}'}
		maxlength="6"
		required
		placeholder="000000"
	/>
</label>

<style>
	.code {
		font-family: var(--mono);
		font-size: 1.4rem;
		letter-spacing: 0.4em;
		text-align: center;
	}
</style>
