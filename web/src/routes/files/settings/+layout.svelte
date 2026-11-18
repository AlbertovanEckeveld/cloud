<script lang="ts">
	import Tabs from '#lib/components/Tabs.svelte';
	import { driveSettingsTabs } from '#lib/drive-settings-tabs.ts';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();
</script>

<Tabs label="Drive-instellingen" tabs={driveSettingsTabs(data.user)} />
{@render children()}
