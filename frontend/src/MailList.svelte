<script>
  import { slide } from 'svelte/transition'

  // selected is a SvelteSet of ids owned by App; current is the open message's id.
  let { items, selected, current, onpick, ontoggle, onstar, ondragstart, onmore, el = $bindable(), empty } = $props()

  const year = new Date().getFullYear()
  const fmtDate = (s) => {
    const d = new Date(s * 1000)
    if (d.toDateString() === new Date().toDateString()) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    return d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', ...(d.getFullYear() !== year && { year: 'numeric' }) })
  }

  // Rows leaving after archive/delete/move fold away; instant when motion is reduced.
  const out = matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 150

  function onscroll(e) {
    const t = e.currentTarget
    if (t.scrollTop + t.clientHeight > t.scrollHeight - 400) onmore()
  }
</script>

<div class="items" class:selecting={selected.size > 0} bind:this={el} {onscroll} role="listbox" aria-multiselectable="true" aria-label="Messages">
  {#each items as m (m.id)}
    <div class="item" class:unread={!m.read} class:open={current === m.id} class:picked={selected.has(m.id)}
      data-id={m.id} role="option" aria-selected={selected.has(m.id) || current === m.id} tabindex="-1" draggable="true"
      onclick={(e) => onpick(m, e)} onkeydown={(e) => e.key === 'Enter' && onpick(m, e)} ondragstart={(e) => ondragstart(e, m)}
      out:slide={{ duration: out }}>
      <input type="checkbox" class="pick" checked={selected.has(m.id)} aria-label="Select"
        onclick={(e) => { e.stopPropagation(); ontoggle(m) }} />
      <button class="star" class:on={m.flagged} aria-label={m.flagged ? 'Unstar' : 'Star'} title="s"
        onclick={(e) => { e.stopPropagation(); onstar(m) }}>{m.flagged ? '★' : '☆'}</button>
      <span class="from">{m.fromName || m.fromAddr || '(unknown sender)'}</span>
      <span class="date">{fmtDate(m.date)}</span>
      <span class="subj">{m.subject || '(no subject)'}</span>
    </div>
  {:else}
    <p class="empty muted">{@render empty()}</p>
  {/each}
</div>

<style>
  .items { overflow-y: auto; flex: 1; }
  .item {
    display: grid; align-items: center; column-gap: 6px; row-gap: var(--row-gap);
    grid-template-columns: auto auto minmax(0, 1fr) auto;
    grid-template-areas: 'pick star from date' 'pick star subj subj';
    padding: var(--row-py) var(--row-px) var(--row-py) 6px; border-bottom: 1px solid var(--rule);
    cursor: default; user-select: none; border-left: 3px solid transparent;
  }
  :global([data-density='compact']) .item {
    grid-template-columns: auto auto minmax(0, 9em) minmax(0, 1fr) auto;
    grid-template-areas: 'pick star from subj date';
  }
  .item:hover { background: color-mix(in srgb, var(--paper) 50%, var(--chrome)); }
  .item:focus-visible { outline: none; } /* j/k and the open highlight show where you are */
  .item.open { background: var(--paper); }
  .item.picked { background: var(--picked); border-left-color: var(--focus); }

  .pick { grid-area: pick; margin: 0; visibility: hidden; }
  .item:hover .pick, .selecting .pick { visibility: visible; }
  .star { grid-area: star; padding: 0 2px; line-height: 1; color: var(--muted); opacity: 0.5; }
  .star:hover { background: none; opacity: 1; }
  .star.on { color: var(--star); opacity: 1; }

  .from, .subj { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .from { grid-area: from; color: var(--muted); }
  .date { grid-area: date; font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .subj { grid-area: subj; color: color-mix(in srgb, var(--ink) 80%, var(--chrome)); }
  .unread .from, .unread .subj { color: var(--ink); font-weight: 600; }
  .empty { padding: 32px 24px; }
</style>
