<script>
  import { slide } from 'svelte/transition'
  import { resize } from './resize.js'

  // selected is a SvelteSet of ids owned by App; current is the open message's id.
  // sort is "col:dir,..." in priority order. The headings set it: click sorts by that
  // column alone (again flips it), Shift+click adds it as the next level, then flips, then drops it.
  // table lines the headings up as columns over the rows.
  let { items, selected, current, onpick, ontoggle, onstar, ondragstart, onmore, el = $bindable(), empty, table, sort, onsort } = $props()

  const year = new Date().getFullYear()
  const fmtDate = (s) => {
    const d = new Date(s * 1000)
    if (d.toDateString() === new Date().toDateString()) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    return d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', ...(d.getFullYear() !== year && { year: 'numeric' }) })
  }

  // Rows leaving after archive/delete/move fold away; instant when motion is reduced.
  const out = matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 150

  const firstDir = { date: 'desc', unread: 'desc', star: 'desc', sender: 'asc', subject: 'asc' }
  let keys = $derived(sort.split(',').map((t) => t.split(':')))
  function sortBy(col, e) {
    const i = keys.findIndex(([c]) => c === col)
    const flip = (d) => (d === 'asc' ? 'desc' : 'asc')
    let next
    if (!e.shiftKey) next = [[col, i === 0 && keys.length === 1 ? flip(keys[0][1]) : firstDir[col]]]
    else if (i < 0) next = [...keys, [col, firstDir[col]]]
    else if (keys[i][1] === firstDir[col]) next = keys.map((k, j) => (j === i ? [col, flip(k[1])] : k))
    else next = keys.filter((_, j) => j !== i)
    onsort(next.length ? next.map((k) => k.join(':')).join(',') : 'date:desc')
  }
  const cols = { unread: 'Unread', star: 'Starred', sender: 'Sender', subject: 'Subject', date: 'Date' }
  const dirNames = { sender: ['A to Z', 'Z to A'], subject: ['A to Z', 'Z to A'], date: ['oldest first', 'newest first'], unread: ['read first', 'unread first'], star: ['unstarred first', 'starred first'] }
  const dirName = (col, d) => dirNames[col][d === 'desc' ? 1 : 0]
  const full = (s) => new Date(s * 1000).toLocaleString('en-GB', { dateStyle: 'full', timeStyle: 'short' })

  function onscroll(e) {
    const t = e.currentTarget
    if (t.scrollTop + t.clientHeight > t.scrollHeight - 400) onmore()
  }
</script>

{#snippet heading(col, text)}
  {@const i = keys.findIndex(([c]) => c === col)}
  <button class:on={i >= 0} onclick={(e) => sortBy(col, e)}
    title="Sort by {cols[col]}. Shift+click adds it as another level; again reverses it, a third time removes it."
    aria-label="Sort by {cols[col]}{i >= 0 ? `, sorted ${dirName(col, keys[i][1])}${keys.length > 1 ? `, level ${i + 1} of ${keys.length}` : ''}` : ''}">
    {text}{#if i >= 0}{keys[i][1] === 'desc' ? ' ↓' : ' ↑'}{#if keys.length > 1}<sup>{i + 1}</sup>{/if}{/if}
  </button>
{/snippet}

<div class="head" class:table>
  <div class="pick">{@render heading('unread', table ? '●' : 'Unread')}</div>
  <div class="star">{@render heading('star', table ? '★' : 'Starred')}</div>
  <div class="from">
    {@render heading('sender', 'Sender')}
    {#if table}<div class="split x" use:resize={{ name: '--col-from', min: 80, max: 600, label: 'Resize the Sender column' }}></div>{/if}
  </div>
  <div class="subj">{@render heading('subject', 'Subject')}</div>
  <div class="date">
    {#if table}<div class="split x" use:resize={{ name: '--col-date', min: 56, max: 240, invert: true, label: 'Resize the Date column' }}></div>{/if}
    {@render heading('date', 'Date')}
  </div>
</div>

<div class="items" class:table class:selecting={selected.size > 0} bind:this={el} {onscroll} role="listbox" aria-multiselectable="true" aria-label="Messages">
  {#each items as m (m.id)}
    <div class="item" class:unread={!m.read} class:open={current === m.id} class:picked={selected.has(m.id)}
      data-id={m.id} role="option" aria-selected={selected.has(m.id) || current === m.id} tabindex="-1" draggable="true"
      onclick={(e) => onpick(m, e)} onkeydown={(e) => e.key === 'Enter' && onpick(m, e)} ondragstart={(e) => ondragstart(e, m)}
      out:slide={{ duration: out }}>
      <input type="checkbox" class="pick" checked={selected.has(m.id)} aria-label="Select"
        onclick={(e) => { e.stopPropagation(); ontoggle(m) }} />
      <button class="star" class:on={m.flagged} aria-label={m.flagged ? 'Unstar' : 'Star'} title="{m.flagged ? 'Unstar' : 'Star'} (s)"
        onclick={(e) => { e.stopPropagation(); onstar(m) }}>{m.flagged ? '★' : '☆'}</button>
      <span class="from" title={m.fromAddr}>{m.fromName || m.fromAddr || '(unknown sender)'}</span>
      <span class="date" title={full(m.date)}>{fmtDate(m.date)}</span>
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
    content-visibility: auto; contain-intrinsic-size: auto 60px; /* skip layout and paint for rows out of view */
  }
  :global([data-density='compact']) .item {
    grid-template-columns: auto auto minmax(0, 9em) minmax(0, 1fr) auto;
    grid-template-areas: 'pick star from subj date';
  }
  /* Table: fixed columns, so rows line up under the headings. */
  .head.table, .items.table .item {
    grid-template-columns: 16px 18px var(--col-from, minmax(0, 1.2fr)) minmax(0, 3fr) var(--col-date, 7.5em);
    grid-template-areas: 'pick star from subj date';
  }
  /* Same scrollbar gutter on both, so the fr columns match. */
  .head, .items.table { overflow-y: hidden; scrollbar-gutter: stable; }
  .items.table { overflow-y: auto; }
  .items.table .item { padding-top: calc(var(--row-py) * 0.6); padding-bottom: calc(var(--row-py) * 0.6); }
  .head {
    display: flex; column-gap: 10px; align-items: center;
    padding: 2px var(--row-px) 2px 6px; border-left: 3px solid transparent; border-bottom: 1px solid var(--rule);
    font-size: 12px; color: var(--muted); user-select: none;
  }
  .head.table { display: grid; column-gap: 6px; }
  .head:not(.table) .date { margin-left: auto; }
  .head > div { position: relative; min-width: 0; overflow: visible; white-space: nowrap; }
  .head .pick, .head .star { visibility: visible; opacity: 1; padding: 0; color: inherit; }
  .head sup { font-size: 9px; }
  .head .date .split { left: -6px; right: auto; }
  .head button { padding: 2px 0; border-radius: 0; text-align: left; color: inherit; white-space: nowrap; }
  .head button:hover { background: none; color: var(--ink); }
  .head .on { color: var(--ink); font-weight: 600; }
  .head .date, .items.table .date { text-align: right; }
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
