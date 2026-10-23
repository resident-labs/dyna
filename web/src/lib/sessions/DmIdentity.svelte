<script lang="ts">
  import Avatar from '../ui/Avatar.svelte'
  import { sessionStatus } from './status.ts'
  import { tokens } from '../ui/format.ts'
  // The DM header's name button: one status label shared with member lists.
  import type { Member } from '../api/room.ts'
  import type { Usage } from '../api/session.ts'
  let { member, usage = null, onopen }: { member: Member; usage?: Usage | null; onopen: (member: Member) => void } = $props()
  let status = $derived(sessionStatus(member))
</script>

<button class="dm-identity" onclick={() => onopen(member)} title="Open profile">
  <Avatar emoji={member.emoji} name={member.name} hue={member.hue} size={32} state={member.state} status={member.status} ring="var(--main)" />
  <span><strong class="hue-{member.hue}">{member.name}</strong><small title="{tokens((usage?.inputTokens || 0) + (usage?.outputTokens || 0))} tokens"><span class:working={status === 'working'}>{status}</span> · {member.profile}</small></span>
</button>

<style>
  .dm-identity { display: flex; align-items: center; gap: 10px; text-align: left; flex: none; }
  .dm-identity > span { display: flex; flex-direction: column; }
  .dm-identity strong { font-size: var(--t-lead); }
  .dm-identity small { color: var(--faint); font-size: var(--t-sm); }
  .working { color: var(--gold); }
  @media (max-width: 760px) {
    .dm-identity { min-width: 0; flex: 1; }
    .dm-identity > span { min-width: 0; }
    .dm-identity strong, .dm-identity small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  }
</style>
