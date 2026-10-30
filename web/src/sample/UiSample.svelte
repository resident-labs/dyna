<script lang="ts">
  // Every shared part in every tone. Neutral comes first in each group:
  // it is the common case, and a colored tone is the exception (rule r16).
  import Card from '../lib/ui/Card.svelte'
  import Chip from '../lib/ui/Chip.svelte'
  import Section from '../lib/ui/Section.svelte'
  import SettingRow from '../lib/ui/SettingRow.svelte'
  import StatTile from '../lib/ui/StatTile.svelte'
  import StatusPill from '../lib/ui/StatusPill.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import type { Tone } from '../lib/ui/tone.ts'

  const tones: Tone[] = ['neutral', 'success', 'warning', 'danger', 'info', 'live']
  let takesWork = $state(true)
  let shared = $state(false)
</script>

<main>
  <Section title="Usage" description="Stat tiles share one flush card. A tone colors the value only when it needs attention.">
    {#snippet action()}<span class="muted">Probed 2 min ago</span>{/snippet}
    <Card flush>
      <div class="stat-grid">
        <StatTile label="CPU" value="81%" detail="load 12.9 on 32 CPUs" tone="warning" meter={81} />
        <StatTile label="Memory" value="46%" detail="22 of 48 GB" meter={46} />
        <StatTile label="Disk free" value="412 GB" detail="floor 20 GB" />
        <StatTile label="Held by work" value="14/16" detail="CPUs reserved" loading />
      </div>
    </Card>
  </Section>

  <Section title="Settings" description="SettingRow in a flush, divided card. Plain facts stay muted text; a pill marks only state that needs attention.">
    <Card flush divided>
      <SettingRow title="Takes work" description="A node that is off takes no new jobs or checks.">
        {#snippet control()}<Switch on={takesWork} label="Takes work" onchange={(on) => (takesWork = on)} />{/snippet}
      </SettingRow>
      <SettingRow title="Rooms that can use it" description="None chosen means every room.">
        {#snippet status()}<StatusPill label="Default" />{/snippet}
        {#snippet control()}<span class="num muted">cozycraft, dyna, horizon</span>{/snippet}
      </SettingRow>
      <SettingRow title="Health probe" description="Dyna probes the node every 5 minutes.">
        {#snippet status()}<StatusPill tone="danger" dot label="Last probe failed" />{/snippet}
        {#snippet control()}<button class="btn">Probe now</button>{/snippet}
      </SettingRow>
      <SettingRow title="Share with other machines" description="Disabled: this row dims and its control is inert." disabled>
        {#snippet control()}<Switch on={shared} label="Share" onchange={(on) => (shared = on)} />{/snippet}
      </SettingRow>
    </Card>
  </Section>

  <Section title="StatusPill" description="Neutral is an outline badge with no wash and no dot. Colored tones are passed on purpose.">
    <Card flush divided>
      {#each tones as tone (tone)}
        <div class="line">
          <span class="micro-label name">{tone}</span>
          <StatusPill {tone} label="Label" />
          <StatusPill {tone} dot label="With dot" />
          <StatusPill {tone} label="Value" value="3/4" />
          <StatusPill {tone} variant="dot" label="Dot variant" />
        </div>
      {/each}
    </Card>
  </Section>

  <Section title="StatTile" description="Each tone, with a meter.">
    <Card flush>
      <div class="stat-grid">
        {#each tones as tone, i (tone)}<StatTile {tone} label={tone} value="{40 + i * 11}%" detail="detail line" meter={40 + i * 11} />{/each}
      </div>
    </Card>
  </Section>

  <Section title="Card">
    <div class="cards">
      <Card title="Plain" count={3}>
        {#snippet action()}<button class="btn">Action</button>{/snippet}
        <p class="muted">Body text with the default padding.</p>
        {#snippet footer()}Footer: a quiet line under a soft border.{/snippet}
      </Card>
      <Card title="Nested">
        <Card title="Inner card goes flat"><p class="muted">No surface and no border of its own.</p></Card>
      </Card>
    </div>
  </Section>

  <Section title="Chip" description="The typed tone on the existing chip. Info uses the info tone; iris stays for actions and focus.">
    <Card>
      <div class="chips">
        {#each tones as tone (tone)}<Chip {tone}>{tone}</Chip>{/each}
      </div>
      <div class="chips">
        {#each tones as tone (tone)}<Chip {tone} outline>{tone}</Chip>{/each}
      </div>
    </Card>
  </Section>

  <Section title="Utilities">
    <Card>
      <div class="line"><span class="micro-label">micro-label</span><span class="num">1,024.50</span><span class="num dimmed">dimmed 88%</span></div>
      <div class="line meters">
        <div class="meter"><span style:width="30%"></span></div>
        <div class="meter"><span style:width="55%" style:background="var(--data-2)"></span></div>
        <div class="meter"><span style:width="70%" style:background="var(--data-3)"></span></div>
        <div class="meter"><span style:width="85%" style:background="var(--data-4)"></span></div>
      </div>
    </Card>
  </Section>
</main>

<style>
  main { height: 100%; overflow: auto; padding: var(--s6); max-width: 64rem; margin: 0 auto; }
  .line { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s3); padding: var(--s3) var(--s4); }
  .chips { display: flex; flex-wrap: wrap; gap: var(--s2); }
  .name { width: 5rem; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(16rem, 1fr)); gap: var(--s4); }
  .meters { display: grid; grid-template-columns: repeat(4, 1fr); }
  .muted { margin: 0; font-size: var(--t-sm); color: var(--muted); }
</style>
