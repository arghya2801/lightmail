<script>
  import { BrowserOpenURL } from '../wailsjs/runtime/runtime'

  let { msg, onkey, zoom, onzoom, onsave, ontoggleread } = $props()

  let frame = $state()

  // Mail is designed for white; keep that whatever the app theme.
  const inject = `<style>html{background:#fff;overflow:hidden}</style>`
  const srcdoc = msg.html.includes('</head>') ? msg.html.replace('</head>', inject + '</head>') : inject + msg.html

  const date = new Date(msg.date * 1000).toLocaleDateString('en-GB', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })

  // Size the frame to its content so the pane scrolls, not the frame. Zoom is applied
  // inside the frame; fixed-width mail zoomed past the pane widens it instead of clipping.
  // Measures from zero (scrollHeight never reports less than the frame) and keeps the reading spot.
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
    new ResizeObserver(fit).observe(doc.body)
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
    <p class="by muted"><span title={msg.fromAddr}>{msg.fromName}</span>, {date}</p>
    <div class="actions">
      <button onclick={onsave} title="Shift+S">Save as HTML</button>
      <button onclick={ontoggleread} title="Shift+U">Mark unread</button>
      <span class="right">
        {#if zoom !== 100}<button class="muted zoom" onclick={() => onzoom(0)} title="Reset zoom (Ctrl+0)">{zoom}%</button>{/if}
      </span>
    </div>
  </header>

  <div class="body" {onwheel}>
    <iframe bind:this={frame} title={msg.subject} sandbox="allow-same-origin" {srcdoc} onload={loaded}></iframe>
  </div>
</article>

<style>
  .sheet {
    max-width: 820px; margin: var(--sheet-m) auto 64px; width: calc(100% - 2 * var(--sheet-m)); background: var(--paper);
    border: 1px solid var(--rule); border-radius: 3px;
  }
  header { padding: var(--head-p); }
  h1 {
    font: 500 var(--h1-size)/1.15 var(--serif); font-optical-sizing: auto; letter-spacing: -0.01em;
    margin: 0 0 10px; text-wrap: balance;
  }
  .by { margin: 0 0 16px; }
  .actions { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; margin-left: -8px; }
  .right { margin-left: auto; display: flex; align-items: center; gap: 8px; font-size: 12px; }
  .zoom { font-size: 12px; font-variant-numeric: tabular-nums; }
  .body { position: relative; }
  iframe { display: block; width: 100%; height: 150px; border: 0; border-top: 1px solid var(--rule); background: #fff; }
</style>
