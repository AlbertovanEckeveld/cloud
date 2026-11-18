<script lang="ts">
	import Tabs from '#lib/components/Tabs.svelte';
	import { identityTabs } from '#lib/identity-tabs.ts';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();
</script>

<Tabs label="Identity" tabs={identityTabs(data.user)} />
{@render children()}
