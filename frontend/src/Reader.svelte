<script>
  import { BrowserOpenURL } from '../wailsjs/runtime/runtime'

  // actions: [{ label, key, run }] for this message, built by App so the bulk panel shares them.
  let { msg, actions, chips, onkey, zoom, onzoom, onsave, onattach, onimages } = $props()

  const kb = (n) => (n < 1024 * 1024 ? Math.max(1, Math.round(n / 1024)) + ' KB' : (n / 1024 / 1024).toFixed(1) + ' MB')

  let frame = $state()

  // Mail is designed for white; keep that whatever the app theme.
  const inject = `<style>html{background:#fff;overflow:hidden}</style>`
  const srcdoc = msg.html.includes('</head>') ? msg.html.replace('</head>', inject + '</head>') : inject + msg.html

  const date = new Date(msg.date * 1000).toLocaleString('en-GB', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit' })

  // Size the frame to its content so the pane scrolls, not the frame. Zoom is applied
  // inside the frame; fixed-width mail zoomed past the pane widens it instead of clipping.
  // Measures from zero (scrollHeight never reports less than the frame) and keeps the reading spot.
  // settled is the body size fit left behind: a resize back to it is fit's own doing, and
  // refitting on it loops forever on mail whose layout depends on the pane's scrollbar.
  let settled = ''
  const size = (b) => `${b.offsetWidth}x${b.offsetHeight}`
  function fit() {
    const el = frame?.contentDocument?.documentElement
    if (!el) return
    const pane = frame.closest('.main')
    const top = pane ? frame.getBoundingClientRect().top - pane.getBoundingClientRect().top + pane.scrollTop : 0
    const read = pane ? pane.scrollTop - top : 0
    const before = frame.offsetHeight
    el.style.zoom = zoom / 100
    frame.style.minWidth = frame.style.height = '0'
    frame.style.minWidth = el.scrollWidth > el.clientWidth ? el.scrollWidth + 'px' : ''
    frame.style.height = el.scrollHeight + 'px'
    if (pane && read > 0 && before) pane.scrollTop = top + (read * frame.offsetHeight) / before
    if (frame.contentDocument.body) settled = size(frame.contentDocument.body)
  }
  $effect(() => {
    zoom
    fit()
  })

  // Ctrl+wheel zooms the message; pixel deltas (touchpads) add up to one step per notch.
  let wheelSum = 0
  function onwheel(e) {
    if (!e.ctrlKey) return
    e.preventDefault()
    wheelSum += e.deltaY
    if (Math.abs(wheelSum) >= 50) {
      onzoom(wheelSum < 0 ? 1 : -1)
      wheelSum = 0
    }
  }

  function loaded() {
    const doc = frame.contentDocument
    fit()
    new ResizeObserver(() => size(doc.body) !== settled && fit()).observe(doc.body)
    doc.addEventListener('wheel', onwheel, { passive: false })
    doc.addEventListener('click', (e) => {
      const a = e.target.closest?.('a[href]')
      if (a) {
        e.preventDefault()
        if (/^(https?|mailto):/i.test(a.href)) BrowserOpenURL(a.href)
      }
    })
    doc.addEventListener('keydown', onkey)
  }
</script>

<article class="sheet">
  <header>
    <h1>{msg.subject || '(no subject)'}</h1>
    <p class="by muted">
      <span class="who">{msg.fromName || msg.fromAddr}</span>{#if msg.fromAddr && msg.fromName !== msg.fromAddr}&ensp;&lt;{msg.fromAddr}&gt;{/if}{#if msg.to}&ensp;to {msg.to}{/if}, {date}
    </p>
    {#if chips.length}
      <p class="chips">{#each chips as c}<span class="chip">{c}</span>{/each}</p>
    {/if}
    {#if msg.attachments?.length}
      <ul class="files" aria-label="Attachments">
        {#each msg.attachments as f, i}
          <li>
            <button onclick={() => onattach(i, f.open)} title="{f.open ? 'Open' : 'Save'} {f.name}">{f.name} <span class="muted">{kb(f.size)}</span></button>
            {#if f.open}<button class="save" onclick={() => onattach(i, false)} title="Save {f.name}">Save</button>{/if}
          </li>
        {/each}
      </ul>
    {/if}
    <div class="actions">
      {#each actions as a (a.label)}<button onclick={a.run} title="{a.label} ({a.key})">{a.label}</button>{/each}
      <span class="right">
        {#if !msg.error && !msg.loading}<button onclick={onsave} title="Save as HTML (Shift+S)">Save as HTML</button>{/if}
        {#if zoom !== 100}<button class="muted zoom" onclick={() => onzoom(0)} title="Reset zoom (Ctrl+0)">{zoom}%</button>{/if}
      </span>
    </div>
  </header>

  {#if msg.blocked}
    <p class="blocked">
      <span class="muted" title="Remote images can tell the sender when you opened a message.">Remote images are hidden.</span>
      <button onclick={() => onimages(false)}>Load images</button>
      <button onclick={() => onimages(true)}>Always for {msg.fromAddr}</button>
    </p>
  {/if}
  <div class="body" {onwheel}>
    {#if msg.error}
      <p class="error">{msg.error}</p>
    {:else if msg.loading}
      <p class="error loading">Downloading the message…</p>
    {:else}
      <iframe bind:this={frame} title={msg.subject} sandbox="allow-same-origin" {srcdoc} onload={loaded}></iframe>
    {/if}
  </div>
</article>

<style>
  .sheet {
    max-width: 820px; margin: var(--sheet-m) auto 64px; width: calc(100% - 2 * var(--sheet-m)); background: var(--paper);
    border: 1px solid var(--rule); border-radius: 3px;
  }
  /* Pinned while the message scrolls under it. */
  header { padding: var(--head-p); position: sticky; top: 0; z-index: 1; background: var(--paper); border-radius: 3px 3px 0 0; }
  h1 {
    font: 500 var(--h1-size)/1.15 var(--serif); font-optical-sizing: auto; letter-spacing: -0.01em;
    margin: 0 0 10px; text-wrap: balance;
  }
  .by { margin: 0 0 12px; }
  .who { color: var(--ink); }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; margin: 0 0 12px; }
  .files { list-style: none; display: flex; flex-wrap: wrap; gap: 6px; margin: 0 0 12px; padding: 0; }
  .files li { display: flex; }
  .files button { border: 1px solid var(--rule); padding: 3px 10px; }
  .files .save { border-left: 0; color: var(--muted); }
  .files .muted { font-size: 12px; margin-left: 4px; }
  .chip { font-size: 12px; padding: 1px 8px; border-radius: 10px; background: var(--chrome); color: var(--muted); }
  .actions { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; margin-left: -8px; }
  .right { margin-left: auto; display: flex; align-items: center; gap: 4px; }
  .zoom { font-size: 12px; font-variant-numeric: tabular-nums; }
  .body { position: relative; }
  .blocked {
    display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin: 0; padding: 8px 40px;
    border-top: 1px solid var(--rule); font-size: 13px;
  }
  .blocked .muted { margin-right: auto; }
  .blocked button { border: 1px solid var(--rule); }
  .error { margin: 0; padding: 16px 40px 24px; border-top: 1px solid var(--rule); color: var(--muted); }
  /* Only says so when the download is slow; a cached body arrives before this shows. */
  .loading { opacity: 0; animation: show 0s 400ms forwards; }
  @keyframes show { to { opacity: 1; } }
  /* Mail is shown on white, so it must see a light theme too: inside the frame,
     prefers-color-scheme follows this color-scheme. Dark-mode rules in mail
     (white links, say) would otherwise land on white. */
  iframe { display: block; width: 100%; height: 150px; border: 0; border-top: 1px solid var(--rule); background: #fff; color-scheme: light; }
</style>
