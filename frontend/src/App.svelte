<script>
  import { onMount } from 'svelte'
  import { SvelteSet } from 'svelte/reactivity'
  import {
    GetConfig, GetSyncStatus, Folders, Senders, List, Open, AllowImages, SetFlags, SaveEmail, SaveAttachment, OpenAttachment, Sync, SetAppearance, SetReaderZoom,
    Archive, Trash, Spam, Move, AddLabel, DeleteForever, Undo, CreateLabel, RenameLabel, DeleteLabel,
  } from '../wailsjs/go/main/App'
  import { EventsOn, WindowSetTitle } from '../wailsjs/runtime/runtime'
  import Reader from './Reader.svelte'
  import MailList from './MailList.svelte'
  import Picker from './Picker.svelte'
  import Settings, { zooms } from './Settings.svelte'
  import { resize } from './resize.js'

  let view = $state('reader') // reader | settings
  let q = $state({ label: '', sender: '', search: '', sort: 'new' })
  let items = $state([])
  let more = true
  let folders = $state([]) // every server folder, with local counts
  let senders = $state([])
  let senderFilter = $state('')
  let msg = $state(null) // the open message
  const selected = new SvelteSet() // ids picked with Ctrl/Shift+click, the checkbox or x
  let anchor = null // last picked row, for Shift+click ranges
  let picker = $state(null) // 'move' | 'label' while the folder picker is open
  let dropOn = $state('') // folder under a drag
  let menu = $state(null) // { x, y, path, folder } while a label's right-click menu is open
  let sync = $state({ running: false, count: 0, error: '' })
  let syncedAt = $state(null)
  let toast = $state(null)
  let question = $state(null) // { text, ok, value, resolve } while ask() waits for an answer
  let helpEl = $state()
  let searchEl = $state()
  let listEl = $state()
  let mainEl = $state()
  let chipEl = $state()

  const sorts = [['new', 'Newest first'], ['old', 'Oldest first'], ['sender', 'Sender A–Z'], ['unread', 'Unread first']]

  // System folders by role, in Gmail's order. User labels sit between them and Spam/Trash.
  const roleNames = { inbox: 'Inbox', flagged: 'Starred', important: 'Important', sent: 'Sent', drafts: 'Drafts', all: 'All Mail', archive: 'Archive', junk: 'Spam', trash: 'Trash' }
  const roleOrder = Object.keys(roleNames)
  const nameOf = (f) => roleNames[f.role] ?? f.name
  let byName = $derived(Object.fromEntries(folders.map((f) => [f.name, f])))
  const label = (name) => (byName[name] ? nameOf(byName[name]) : name)
  const roleFolder = (role) => folders.find((f) => f.role === role)?.name ?? ''
  const noCount = new Set(['sent', 'drafts', 'all', 'archive', 'junk', 'trash'])

  // Searching spans all mail, like Gmail; actions then work on each message where it lives.
  let from = $derived(q.search ? '' : q.label)
  let role = $derived(byName[from]?.role ?? '')

  let synced = $derived(folders.filter((f) => f.synced))
  let system = $derived(synced.filter((f) => f.role && f.role !== 'junk' && f.role !== 'trash').sort((a, b) => roleOrder.indexOf(a.role) - roleOrder.indexOf(b.role)))
  let bin = $derived(synced.filter((f) => f.role === 'junk' || f.role === 'trash').sort((a, b) => roleOrder.indexOf(a.role) - roleOrder.indexOf(b.role)))

  // User labels as a tree: "Work/Clients/Acme" nests under Work. Parents that
  // aren't folders themselves still show, so the branch can fold.
  let collapsed = new SvelteSet(stored('collapsed', []))
  function stored(key, fallback) {
    try {
      return JSON.parse(localStorage.getItem(key)) ?? fallback
    } catch {
      return fallback
    }
  }
  function fold(path) {
    collapsed.has(path) ? collapsed.delete(path) : collapsed.add(path)
    try {
      localStorage.setItem('collapsed', JSON.stringify([...collapsed]))
    } catch {}
  }
  const cmpPath = (a, b) => {
    const x = a.split('/'), y = b.split('/')
    for (let i = 0; i < Math.min(x.length, y.length); i++) {
      const c = x[i].localeCompare(y[i])
      if (c) return c
    }
    return x.length - y.length
  }
  let tree = $derived.by(() => {
    const rows = []
    const seen = new Set()
    for (const f of synced.filter((f) => !f.role).sort((a, b) => cmpPath(a.name, b.name))) {
      const parts = f.name.split('/')
      for (let i = 1; i <= parts.length; i++) {
        const path = parts.slice(0, i).join('/')
        if (seen.has(path)) continue
        seen.add(path)
        rows.push({ path, text: parts[i - 1], depth: i - 1, folder: byName[path]?.synced ? byName[path] : null })
      }
    }
    for (const r of rows) r.kids = rows.some((o) => o.path.startsWith(r.path + '/'))
    return rows.filter((r) => ![...collapsed].some((c) => r.path.startsWith(c + '/')))
  })

  let visibleSenders = $derived(
    // Empty filter: only the sender being viewed, so it can still be cleared.
    senderFilter ? senders.filter((s) => (s.name + s.key).toLowerCase().includes(senderFilter.toLowerCase())) : senders.filter((s) => s.key === q.sender),
  )

  // Rows taken out by an action stay out of reloads that may have read the cache
  // before the action finished: id -> null while it runs, then the number of the
  // last load started by then. A load started later reads the result itself.
  const hidden = new Map()
  let loads = 0
  let shown = 0
  async function load(reset, keepScroll) {
    const n = ++loads
    let page = (await List({ ...q, label: from, offset: reset ? 0 : items.length })) ?? []
    if (n < shown) return // a newer load already landed
    shown = n
    page = page.filter((m) => !hidden.has(m.id) || (hidden.get(m.id) !== null && n > hidden.get(m.id)))
    for (const [id, last] of hidden) if (last !== null && n > last) hidden.delete(id)
    items = reset ? page : [...items, ...page]
    more = page.length === 100
    if (reset && !keepScroll && listEl) listEl.scrollTop = 0
  }

  let chosen = false // picked the Inbox on first load
  async function counts() {
    folders = (await Folders()) ?? []
    const unread = folders.find((f) => f.role === 'inbox')?.unread
    WindowSetTitle(unread ? `(${unread}) Lightmail` : 'Lightmail') // shows on the taskbar
    if (!chosen && folders.length) {
      chosen = true
      const inbox = synced.find((f) => f.role === 'inbox')
      if (inbox && !q.label) q.label = inbox.name
    }
    senders = (await Senders(from)) ?? []
  }

  // Reload the list when any filter changes; search is debounced.
  let timer
  $effect(() => {
    const { label, sender, search, sort } = q
    clearTimeout(timer)
    timer = setTimeout(() => load(true), search ? 200 : 0)
  })
  $effect(() => {
    Senders(from).then((s) => (senders = s ?? []))
  })

  function go(name) {
    q.label = name
    q.sender = ''
    q.search = ''
    view = 'reader'
    selected.clear()
  }

  // Toasts fade after 6 seconds unless hovered; errors stay until dismissed.
  function flash(text, action, fn) {
    toast = { text, action, fn }
    later()
  }
  function fail(text) {
    clearTimeout(flash.t)
    toast = { text, action: 'Dismiss', fn: () => {}, error: true }
  }
  function later() {
    clearTimeout(flash.t)
    if (!toast?.error) flash.t = setTimeout(() => (toast = null), 6000)
  }

  // In-app stand-in for confirm() and prompt(): resolves to true, the typed text, or null.
  function ask(text, ok, value = null) {
    return new Promise((resolve) => (question = { text, ok, value, resolve }))
  }
  function answered(e) {
    const { value, resolve } = question
    question = null
    resolve(e.currentTarget.returnValue === 'ok' ? (value ?? true) : null)
  }
  const modal = (d) => d.showModal()

  // Show the headers at once; the body follows when it has downloaded.
  async function open(m) {
    view = 'reader'
    msg = { ...$state.snapshot(m), html: '', labels: [], loading: true }
    let full
    try {
      full = await Open(m.id, false)
    } catch (e) {
      // Keep the headers, so the message can be triaged while offline.
      if (msg?.id === m.id) msg = { ...msg, loading: false, error: "Couldn't download this message. " + e }
      return
    }
    if (msg?.id === m.id) msg = full // unless another message was opened meanwhile
    if (!m.read) {
      m.read = true
      counts()
    }
  }

  function step(d) {
    const i = items.findIndex((m) => m.id === msg?.id)
    const next = items[i + d] ?? (i < 0 ? items[0] : null)
    if (next) {
      open(next)
      listEl?.querySelector(`[data-id="${next.id}"]`)?.scrollIntoView({ block: 'nearest' })
    }
    if (more && i + d >= items.length - 5) load(false)
  }

  // Click opens; Ctrl+click toggles one row; Shift+click selects a range.
  function pick(m, e) {
    if (e.shiftKey && anchor != null) {
      const a = items.findIndex((x) => x.id === anchor), b = items.findIndex((x) => x.id === m.id)
      for (const x of items.slice(Math.min(a, b), Math.max(a, b) + 1)) selected.add(x.id)
      return
    }
    if (e.ctrlKey || e.metaKey) {
      if (!selected.size && msg) selected.add(msg.id)
      return toggle(m)
    }
    selected.clear()
    anchor = m.id
    open(m)
  }
  function toggle(m) {
    selected.has(m.id) ? selected.delete(m.id) : selected.add(m.id)
    anchor = m.id
  }

  // What an action applies to: the selection, else the open message.
  const targets = () => (selected.size ? [...selected] : msg ? [msg.id] : [])
  const rowsOf = (ids) => {
    const rows = items.filter((m) => ids.includes(m.id))
    return rows.length || !msg ? rows : [msg]
  }

  // Take rows out of the list straight away and open the next one; the server catches up.
  function takeOut(ids) {
    const gone = new Set(ids)
    const i = items.findIndex((m) => gone.has(m.id))
    const openGone = msg && gone.has(msg.id)
    items = items.filter((m) => !gone.has(m.id))
    selected.clear()
    if (openGone) {
      const next = items[i] ?? items[i - 1]
      next ? open(next) : (msg = null)
    }
  }

  const done = {
    archive: () => 'Archived', trash: () => 'Moved to Trash', spam: () => 'Marked as spam', notspam: () => 'Moved to Inbox',
    move: (to) => `Moved to ${label(to)}`, label: (to) => `Added label ${label(to)}`, forever: () => 'Deleted forever',
  }

  const failed = {
    archive: () => 'archive', trash: () => 'delete', spam: () => 'mark as spam', notspam: () => 'move to Inbox',
    move: (to) => `move to ${label(to)}`, label: (to) => `add label ${label(to)}`, forever: () => 'delete forever',
  }

  async function act(kind, to = '', ids = targets()) {
    if (!ids.length) return
    const n = ids.length
    if (kind === 'forever' && !(await ask(`Delete ${n === 1 ? 'this message' : `${n} messages`} forever? This can't be undone.`, 'Delete forever'))) return
    const src = from
    if (kind !== 'label') {
      for (const id of ids) hidden.set(id, null)
      takeOut(ids)
    }
    const finished = () => kind !== 'label' && ids.forEach((id) => hidden.set(id, loads))
    let moved = null
    try {
      if (kind === 'archive') moved = await Archive(ids, src)
      else if (kind === 'trash') moved = await Trash(ids, src)
      else if (kind === 'spam') moved = await Spam(ids, src)
      else if (kind === 'notspam') moved = await Move(ids, src, roleFolder('inbox'))
      else if (kind === 'move') moved = await Move(ids, src, to)
      else if (kind === 'label') moved = await AddLabel(ids, to)
      else if (kind === 'forever') await DeleteForever(ids, src)
    } catch (e) {
      finished()
      fail(`Couldn't ${failed[kind](to)}. ${e}`)
      load(true, true)
      return counts()
    }
    finished()
    counts()
    const undo = moved?.length && moved.every((m) => m.uid)
    flash(`${done[kind](to)}${n > 1 ? `: ${n} messages` : ''}.`, undo && 'Undo', async () => {
      try {
        await Undo(moved)
      } catch (e) {
        fail("Couldn't undo. " + e)
      }
      load(true, true)
      counts()
    })
  }

  async function flag(flag, key, value) {
    const ids = targets()
    if (!ids.length) return
    const rows = rowsOf(ids)
    value ??= rows.some((m) => !m[key])
    await SetFlags(ids, flag, value)
    for (const m of rows) m[key] = value
    if (msg && ids.includes(msg.id)) msg[key] = value
    if (key === 'read') counts()
  }
  const setRead = (value) => flag('\\Seen', 'read', value)
  const setStar = (value) => flag('\\Flagged', 'flagged', value)
  async function star(m) {
    await SetFlags([m.id], '\\Flagged', !m.flagged)
    m.flagged = !m.flagged
    if (msg?.id === m.id) msg.flagged = m.flagged
  }

  async function save() {
    if (!msg) return
    try {
      flash('Saved to ' + (await SaveEmail(msg.id)))
    } catch (e) {
      fail("Couldn't save. " + e)
    }
  }

  async function loadImages(always) {
    try {
      if (always) await AllowImages(msg.fromAddr)
      msg = await Open(msg.id, true)
    } catch (e) {
      fail("Couldn't load images. " + e)
    }
  }

  async function attachment(i, open) {
    try {
      if (open) return await OpenAttachment(msg.id, i)
      const path = await SaveAttachment(msg.id, i)
      if (path) flash('Saved to ' + path)
    } catch (e) {
      fail(`Couldn't ${open ? 'open' : 'save'} the attachment. ` + e)
    }
  }

  // Keys follow Gmail where Gmail has one.
  const archive = () => role !== 'trash' && role !== 'junk' && role !== 'all' && act('archive')
  const remove = () => act(role === 'trash' || role === 'junk' ? 'forever' : 'trash')
  const spam = () => act(role === 'junk' ? 'notspam' : 'spam')

  let actions = $derived.by(() => {
    const rows = rowsOf(selected.size ? [...selected] : msg ? [msg.id] : [])
    const a = []
    if (role === 'trash') a.push(['Delete forever', '#', remove], ['Move to Inbox', '', () => act('move', roleFolder('inbox'))])
    else if (role === 'junk') a.push(['Not spam', '!', spam], ['Delete forever', '#', remove])
    else {
      if (role !== 'all') a.push(['Archive', 'e', archive])
      a.push(['Delete', '#', remove], ['Spam', '!', spam])
    }
    a.push(
      ['Move to…', 'v', () => (picker = 'move')],
      ['Label…', 'c', () => (picker = 'label')],
      rows.some((m) => !m.read) ? ['Mark read', 't', () => setRead(true)] : ['Mark unread', 't', () => setRead(false)],
      rows.some((m) => !m.flagged) ? ['Star', 's', () => setStar(true)] : ['Unstar', 's', () => setStar(false)],
    )
    return a.map(([label, key, run]) => ({ label, key, run }))
  })

  let pickable = $derived(
    folders
      .filter((f) => f.name !== from && f.role !== 'all' && f.role !== 'important' && f.role !== 'flagged' && f.role !== 'drafts')
      .map((f) => ({ name: f.name, label: nameOf(f) }))
      .sort((a, b) => a.label.localeCompare(b.label)),
  )

  // Drag rows onto a folder: drop moves there, Ctrl+drop adds the label.
  // Trash, Spam and All Mail run delete, spam and archive.
  const DT = 'application/x-lightmail-ids'
  function dragstart(e, m) {
    const ids = selected.has(m.id) ? [...selected] : [m.id]
    e.dataTransfer.setData(DT, JSON.stringify(ids))
    e.dataTransfer.effectAllowed = 'copyMove'
    chipEl.textContent = ids.length === 1 ? m.subject || '1 message' : `${ids.length} messages`
    e.dataTransfer.setDragImage(chipEl, -14, -10)
  }
  function dragover(e, f) {
    if (!e.dataTransfer.types.includes(DT) || f.name === from) return
    e.preventDefault()
    e.dataTransfer.dropEffect = e.ctrlKey && !f.role ? 'copy' : 'move'
    dropOn = f.name
  }
  function dropped(e, f) {
    e.preventDefault()
    dropOn = ''
    const ids = JSON.parse(e.dataTransfer.getData(DT) || '[]')
    if (f.role === 'trash') act('trash', '', ids)
    else if (f.role === 'junk') act('spam', '', ids)
    else if (f.role === 'all' || f.role === 'archive') act('archive', '', ids)
    else act(e.ctrlKey && !f.role ? 'label' : 'move', f.name, ids)
  }

  // Labels: create, rename (nested ones follow, as in Gmail) and delete.
  async function labelOp(what, fn) {
    try {
      await fn()
      await counts()
    } catch (e) {
      fail(`Couldn't ${what}. ${e}`)
    }
  }
  function openMenu(e, path, folder) {
    e.preventDefault()
    e.stopPropagation()
    const z = look.scale / 100 // app zoom scales left/top too; clientX/Y are already zoomed
    menu = { x: e.clientX / z, y: e.clientY / z, path, folder }
  }
  async function newLabel(parent = '') {
    const n = (await ask(parent ? `New label inside ${parent}` : 'New label', 'Create', ''))?.trim()
    if (n) labelOp('create the label', () => CreateLabel(parent ? `${parent}/${n}` : n))
  }
  async function renameLabel(old) {
    const n = (await ask('Rename label', 'Rename', old))?.trim()
    if (!n || n === old) return
    labelOp('rename the label', async () => {
      await RenameLabel(old, n)
      if (q.label === old || q.label.startsWith(old + '/')) q.label = n + q.label.slice(old.length)
    })
  }
  async function deleteLabel(name) {
    if (!(await ask(`Delete the label ${name}? Its messages keep their other labels and stay in All Mail.`, 'Delete label'))) return
    labelOp('delete the label', async () => {
      await DeleteLabel(name)
      if (q.label === name) go(roleFolder('inbox'))
    })
  }

  // Ctrl+Shift +/-/0 zooms the whole app; plain Ctrl +/-/0 (and Ctrl+wheel) only the message.
  const zoomKeys = { Equal: 1, NumpadAdd: 1, Minus: -1, NumpadSubtract: -1, Digit0: 0, Numpad0: 0 }
  const readerZooms = [50, 67, 80, 90, 100, 110, 125, 150, 175, 200]
  const zoomStep = (levels, cur, d) => {
    const i = levels.indexOf(cur)
    return d === 0 ? 100 : levels[Math.min(Math.max((i < 0 ? levels.indexOf(100) : i) + d, 0), levels.length - 1)]
  }
  const zoomApp = (d) => setLook({ scale: zoomStep(zooms, look.scale, d) })
  let readerZoom = $state(100)
  function zoomReader(d) {
    readerZoom = zoomStep(readerZooms, readerZoom, d)
    SetReaderZoom(readerZoom)
  }

  // Folders in sidebar order, for g 1–9 and the Go to picker.
  let sideOrder = $derived([
    ...system.map((f) => f.name),
    ...(system.some((f) => f.role === 'all') ? [] : ['']),
    ...tree.filter((r) => r.folder).map((r) => r.path),
    ...bin.map((f) => f.name),
  ])

  // Space pages through the open message, then moves to the next one.
  function page(d) {
    const el = mainEl
    if (!el || !msg) return d > 0 && step(1)
    if (d > 0 && el.scrollTop + el.clientHeight >= el.scrollHeight - 2) return step(1)
    el.scrollBy({ top: d * el.clientHeight * 0.85 })
  }

  let gPending = false
  function onkey(e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.code in zoomKeys) {
      if (e.shiftKey) zoomApp(zoomKeys[e.code])
      else if (view === 'reader' && msg) zoomReader(zoomKeys[e.code])
      else return
      return e.preventDefault()
    }
    if (picker || document.querySelector('dialog[open]')) return
    if (menu && e.key === 'Escape') return (menu = null)
    if (e.target?.closest?.('input, textarea, select')) {
      if (e.key === 'Escape') e.target.blur()
      return
    }
    if ((e.ctrlKey || e.metaKey) && e.key === 'a' && view === 'reader') {
      e.preventDefault()
      for (const m of items) selected.add(m.id)
      return
    }
    if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
      e.preventDefault()
      return searchEl?.focus()
    }
    if (e.ctrlKey || e.metaKey || e.altKey) return
    if (gPending) {
      gPending = false
      e.preventDefault() // or the f of g f lands in the picker it opens
      if (e.key === 'i' && roleFolder('inbox')) go(roleFolder('inbox'))
      if (e.key === 's') view = 'settings'
      if (e.key === 'f' || e.key === 'l') picker = 'go'
      if (e.key >= '1' && e.key <= '9' && sideOrder[e.key - 1] !== undefined) go(sideOrder[e.key - 1])
      return
    }
    const keys = {
      j: () => step(1),
      k: () => step(-1),
      f: () => step(1), // f / d: next / previous, for the left hand
      d: () => step(-1),
      ' ': () => page(e.shiftKey ? -1 : 1),
      x: () => msg && toggle(msg),
      e: archive,
      '#': remove,
      Delete: remove,
      '!': spam,
      v: () => targets().length && (picker = 'move'),
      l: () => targets().length && (picker = 'label'),
      c: () => targets().length && (picker = 'label'),
      t: () => setRead(),
      T: () => setLook({ layout: look.layout === 'table' ? 'cards' : 'table' }),
      z: () => toast?.action && (toast.fn(), (toast = null)),
      s: () => setStar(),
      I: () => setRead(true),
      U: () => setRead(false),
      S: save,
      r: Sync,
      '/': () => searchEl?.focus(),
      g: () => (gPending = true),
      '?': () => helpEl.showModal(),
      Escape: () => selected.clear(),
    }
    if (keys[e.key]) {
      e.preventDefault()
      keys[e.key]()
    }
  }

  // The ? sheet. Keep in step with the README table.
  const keyHelp = [
    ['f / j', 'Next message'], ['d / k', 'Previous message'], ['Space / Shift+Space', 'Page through the message, then open the next'],
    ['e', 'Archive'], ['# / Delete', 'Delete (in Trash or Spam: delete forever)'], ['!', 'Report spam (in Spam: not spam)'],
    ['z', 'Undo, while its toast is showing'], ['v', 'Move to…'], ['c / l', 'Add label…'], ['s', 'Star or unstar'],
    ['t', 'Mark read or unread'], ['x', 'Select the open message'], ['Ctrl+A / Esc', 'Select all / clear the selection'],
    ['Ctrl+F / /', 'Search all mail'], ['g 1 … g 9', 'Open the nth folder in the sidebar'], ['g f / g l', 'Go to any folder or label'],
    ['g i', 'Inbox'], ['g s', 'Settings'], ['Shift+T', 'Switch between Cards and Table'], ['r', 'Sync now'],
    ['Shift+S', 'Save the message as HTML'], ['Ctrl+Plus / Minus / 0', 'Zoom the message'], ['Ctrl+Shift+Plus / Minus / 0', 'Zoom the whole app'],
  ]

  // Open the label menu leftward or upward when it would run off the window.
  function flip(el) {
    const r = el.getBoundingClientRect()
    el.style.transform = `translate(${r.right > innerWidth ? '-100%' : 0}, ${r.bottom > innerHeight ? '-100%' : 0})`
  }

  // Density, layout and text size apply straight away, so Settings can preview them live.
  let look = $state({ density: 'comfortable', layout: 'cards', scale: 100 })
  function appearance(l) {
    look = { density: l.density || 'comfortable', layout: l.layout === 'table' ? 'table' : 'cards', scale: l.scale || 100 }
    const root = document.documentElement
    root.dataset.density = look.density
    root.style.zoom = look.scale / 100
    root.style.setProperty('--scale', look.scale / 100)
  }
  function setLook(change) {
    appearance({ ...look, ...change })
    SetAppearance(look.density, look.layout, look.scale)
  }

  onMount(async () => {
    const cfg = await GetConfig()
    appearance(cfg)
    readerZoom = cfg.readerZoom || 100
    if (!cfg.password || !cfg.labels?.length) view = 'settings'
    counts()
    sync = await GetSyncStatus() // the first sync starts before this page listens
    return EventsOn('sync', (s) => {
      sync = s
      if (!s.running) syncedAt = new Date()
      if (!s.running || items.length < 100) {
        counts()
        load(true, true)
      }
    })
  })
</script>

<svelte:window onkeydown={onkey} onclick={() => (menu = null)} />

{#snippet folderRow(f, text)}
  <button class="row" class:on={view === 'reader' && q.label === f.name && !q.search} class:drop={dropOn === f.name} title={f.name}
    onclick={() => go(f.name)} ondragover={(e) => dragover(e, f)} ondragleave={() => dropOn === f.name && (dropOn = '')}
    ondrop={(e) => dropped(e, f)} oncontextmenu={(e) => !f.role && openMenu(e, f.name, f)}>
    <span>{text}</span>{#if f.unread && !noCount.has(f.role)}<span class="n">{f.unread}</span>{/if}
  </button>
{/snippet}

<div class="app" class:wide={view !== 'reader'} class:table={look.layout === 'table'}>
  <nav class="side">
    <div class="split x" use:resize={{ name: '--side-w', min: 150, max: 480, label: 'Resize the sidebar' }}></div>
    <div class="scroll">
      {#each system as f (f.name)}
        <div class="branch">{@render folderRow(f, nameOf(f))}</div>
      {/each}
      {#if !system.some((f) => f.role === 'all')}
        <div class="branch">
          <button class="row" class:on={view === 'reader' && !q.label && !q.search} onclick={() => go('')}><span>All mail</span></button>
        </div>
      {/if}
      <div class="labels-head">
        <span class="muted">Labels</span>
        <button onclick={() => newLabel()} title="New label" aria-label="New label">+</button>
      </div>
      {#if tree.length}
        {#each tree as r (r.path)}
          <div class="branch tree" style:--depth={r.depth}>
            {#if r.kids}
              <button class="twisty" aria-expanded={!collapsed.has(r.path)} aria-label="{collapsed.has(r.path) ? 'Expand' : 'Collapse'} {r.path}"
                class:shut={collapsed.has(r.path)} onclick={() => fold(r.path)}>›</button>
            {/if}
            {#if r.folder}{@render folderRow(r.folder, r.text)}{:else}<span class="row virtual" role="presentation" oncontextmenu={(e) => openMenu(e, r.path, null)}>{r.text}</span>{/if}
          </div>
        {/each}
      {/if}
      {#if bin.length}
        <div class="gap"></div>
        {#each bin as f (f.name)}
          <div class="branch">{@render folderRow(f, nameOf(f))}</div>
        {/each}
      {/if}

      <div class="senders-head">
        <span class="muted">Senders</span>
        <input placeholder="Filter" aria-label="Filter senders" bind:value={senderFilter} />
      </div>
      {#each visibleSenders as s (s.key)}
        <button class="row sender" class:on={q.sender === s.key} title={s.key}
          onclick={() => ((q.sender = q.sender === s.key ? '' : s.key), (view = 'reader'))}>
          <span>{s.name}</span>{#if s.unread}<span class="n">{s.unread}</span>{/if}
        </button>
      {/each}
    </div>

    <div class="foot">
      <button class="row" class:on={view === 'settings'} onclick={() => (view = 'settings')} title="Settings (g s)">Settings</button>
      <div class="sync">
        <span class="muted" class:warn={sync.error && !sync.running} title={sync.error || null} role="status">
          {#if sync.running}Syncing{sync.label ? ` ${label(sync.label)}` : ''}… {sync.count ? `${sync.count} new` : ''}
          {:else if sync.error}Sync failed: {sync.error}
          {:else if syncedAt}Synced {syncedAt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
          {/if}
        </span>
        <button onclick={Sync} disabled={sync.running} title="Sync now (r)">Sync now</button>
      </div>
    </div>
  </nav>

  {#if view === 'reader'}
    <section class="list">
      {#if look.layout === 'table'}
        <div class="split y" use:resize={{ name: '--list-h', min: 120, max: 2000, y: true, label: 'Resize the message list' }}></div>
      {:else}
        <div class="split x" use:resize={{ name: '--list-w', min: 260, max: 1000, label: 'Resize the message list' }}></div>
      {/if}
      <div class="tools">
        <input type="search" placeholder="Search all mail" aria-label="Search all mail" bind:this={searchEl} bind:value={q.search} />
        <select bind:value={q.sort} aria-label="Sort">
          {#each sorts as [v, text]}<option value={v}>{text}</option>{/each}
        </select>
        <div class="layout" role="radiogroup" aria-label="Layout">
          {#each [['cards', 'Cards beside the message', 'M2 3h12M2 6h8M2 10h12M2 13h8'], ['table', 'Table above the message', 'M1.5 3h13M1.5 6.5h13M1.5 10h13M1.5 13.5h13M5.5 1.5v13']] as [v, text, d]}
            <button role="radio" aria-checked={look.layout === v} aria-label={text} title={text} class:on={look.layout === v}
              onclick={() => setLook({ layout: v })}>
              <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path {d} stroke="currentColor" stroke-width="1.3" fill="none" /></svg>
            </button>
          {/each}
        </div>
      </div>
      <MailList {items} {selected} current={msg?.id} bind:el={listEl} onpick={pick} ontoggle={toggle} onstar={star} ondragstart={dragstart}
        table={look.layout === 'table'} sort={q.sort} onsort={(s) => (q.sort = s)}
        onmore={() => more && ((more = false), load(false))}>
        {#snippet empty()}
          {#if q.search || q.sender}Nothing matches. Clear the search or pick another sender.
          {:else if sync.running}Fetching your mail…
          {:else if !folders.length}No mail here yet. Pick your folders in Settings, then sync.
          {:else}Nothing in {q.label ? label(q.label) : 'All mail'}.{/if}
        {/snippet}
      </MailList>
    </section>
  {/if}

  <main class="main" bind:this={mainEl}>
    {#if view === 'settings'}
      <Settings onsaved={() => { view = 'reader'; Sync() }} {look} onlook={setLook} />
    {:else if selected.size > 1}
      <div class="bulk">
        <h1>{selected.size} selected</h1>
        <div class="actions">
          {#each actions as a (a.label)}<button onclick={a.run} title="{a.label} ({a.key})">{a.label}</button>{/each}
        </div>
        <button class="muted" onclick={() => selected.clear()} title="Clear selection (Esc)">Clear selection</button>
      </div>
    {:else if msg}
      {#key `${msg.id}:${msg.blocked}:${msg.loading}`}
        <Reader {msg} {onkey} zoom={readerZoom} onzoom={zoomReader}
          {actions} onsave={save} onattach={attachment} onimages={loadImages}
          chips={(msg.labels ?? []).filter((l) => l !== from).map(label)} />
      {/key}
    {:else}
      <p class="empty muted">Pick a message {look.layout === 'table' ? 'above' : 'on the left'}, or press f. Press ? for every key.</p>
    {/if}
  </main>

  {#if picker}
    <Picker title={{ move: 'Move to', label: 'Add label', go: 'Go to' }[picker]}
      folders={picker === 'go' ? sideOrder.map((name) => ({ name, label: name ? label(name) : 'All mail' }))
        : picker === 'move' ? pickable : pickable.filter((f) => !byName[f.name]?.role)}
      onclose={() => (picker = null)} onpick={(name) => { const kind = picker; picker = null; kind === 'go' ? go(name) : act(kind, name) }} />
  {/if}

  {#if toast}
    <div class="toast" class:error={toast.error} role={toast.error ? 'alert' : 'status'} onmouseenter={() => clearTimeout(flash.t)} onmouseleave={later}>
      <span>{toast.text}</span>
      {#if toast.action}<button onclick={() => { toast.fn(); toast = null }}>{toast.action}</button>{/if}
    </div>
  {/if}

  {#if menu}
    <div class="menu" role="menu" use:flip style:left="{menu.x}px" style:top="{menu.y}px">
      <button role="menuitem" onclick={() => newLabel(menu.path)}>New label inside…</button>
      {#if menu.folder}
        <button role="menuitem" onclick={() => renameLabel(menu.path)}>Rename…</button>
        <button role="menuitem" onclick={() => deleteLabel(menu.path)}>Delete label</button>
      {/if}
    </div>
  {/if}

  {#if question}
    <dialog class="ask" use:modal onclose={answered}>
      <form method="dialog">
        <p>{question.text}</p>
        {#if question.value != null}<input bind:value={question.value} aria-label={question.text} />{/if}
        <div class="buttons">
          <button type="button" onclick={(e) => e.currentTarget.closest('dialog').close()}>Cancel</button>
          <button class="primary" value="ok">{question.ok}</button>
        </div>
      </form>
    </dialog>
  {/if}

  <dialog class="help" bind:this={helpEl} aria-label="Keyboard shortcuts" onclick={(e) => e.target === e.currentTarget && helpEl.close()}>
    <div>
      <h2>Keyboard shortcuts</h2>
      <table>
        <tbody>{#each keyHelp as [k, what]}<tr><td><kbd>{k}</kbd></td><td>{what}</td></tr>{/each}</tbody>
      </table>
      <p class="muted">In Move, Label and Go to, type to filter, Tab through the folders and Space to pick. Esc closes.</p>
    </div>
  </dialog>

  <div class="dragchip" bind:this={chipEl} aria-hidden="true"></div>
</div>

<style>
  .app {
    display: grid;
    grid-template-columns: var(--side-w) var(--list-w) 1fr;
    height: calc(100vh / var(--scale, 1)); /* zoom shrinks vh; undo it */
  }
  .app.wide { grid-template-columns: var(--side-w) 1fr; }
  /* Table layout: the list spans the width above the message. */
  .app.table:not(.wide) { grid-template-columns: var(--side-w) 1fr; grid-template-rows: auto minmax(0, 1fr); }
  .app.table:not(.wide) .side { grid-row: span 2; }
  .app.table:not(.wide) .list {
    height: var(--list-h, calc(42vh / var(--scale, 1))); max-height: calc(85vh / var(--scale, 1));
    border-right: 0; border-bottom: 1px solid var(--rule);
  }

  .side, .list { position: relative; }
  .side {
    display: flex; flex-direction: column; min-height: 0;
    padding: 16px 8px 8px; border-right: 1px solid var(--rule);
  }
  .scroll { flex: 1; overflow-y: auto; min-height: 0; }
  .branch { display: flex; align-items: center; }
  .branch.tree { padding-left: calc(18px + var(--depth) * 12px); }
  .twisty { flex: none; width: 18px; padding: 0; margin-left: -18px; color: var(--muted); font-size: 15px; line-height: 1; transform: rotate(90deg); }
  .twisty.shut { transform: none; }
  .gap { height: 12px; }
  .labels-head { display: flex; align-items: center; justify-content: space-between; padding: 14px 4px 2px 10px; }
  .labels-head button { padding: 0 6px; color: var(--muted); font-size: 16px; line-height: 1.2; }
  .menu {
    position: fixed; z-index: 15; display: flex; flex-direction: column; min-width: 180px; padding: 4px 0;
    background: var(--paper); border: 1px solid var(--rule); border-radius: 6px;
    box-shadow: 0 8px 24px color-mix(in srgb, var(--ink) 16%, transparent);
  }
  .menu button { text-align: left; border-radius: 0; padding: 5px 14px; }
  .row {
    display: flex; justify-content: space-between; gap: 8px; width: 100%; min-width: 0;
    text-align: left; padding: var(--nav-py) 10px;
  }
  .row span:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row.on { background: var(--paper); }
  .row.sender { content-visibility: auto; contain-intrinsic-size: auto 28px; }
  .row.virtual { color: var(--muted); }
  .row.drop { background: var(--drop); box-shadow: inset 0 0 0 1px var(--focus); }
  .n { color: var(--muted); font-variant-numeric: tabular-nums; }
  .senders-head { display: flex; align-items: center; gap: 8px; padding: 20px 10px 6px; }
  .senders-head input { flex: 1; min-width: 0; padding: 2px 6px; }
  .foot { border-top: 1px solid var(--rule); padding-top: 8px; }
  .sync { display: flex; align-items: center; justify-content: space-between; gap: 6px; padding: 6px 10px 0; font-size: 12px; }
  .sync span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sync button { flex: none; font-size: 12px; }
  .sync .warn { color: var(--warn); }

  .list { display: flex; flex-direction: column; min-height: 0; border-right: 1px solid var(--rule); }
  .tools { display: flex; gap: 8px; padding: var(--tools-p); border-bottom: 1px solid var(--rule); }
  .tools input { flex: 1; min-width: 0; }
  .layout { display: flex; flex: none; border: 1px solid var(--rule); border-radius: 4px; background: var(--paper); }
  .layout button { display: grid; place-items: center; padding: 0 6px; border-radius: 0; color: var(--muted); }
  .layout button.on { background: var(--picked); color: var(--focus); }

  .main { overflow-y: auto; min-height: 0; min-width: 0; scrollbar-gutter: stable; } /* fixed width while a message is measured */
  .empty { padding: 32px 24px; }
  .bulk { max-width: 820px; margin: var(--sheet-m) auto; padding: var(--head-p); display: flex; flex-direction: column; gap: 12px; align-items: flex-start; }
  .bulk h1 { font: 500 var(--h1-size)/1.15 var(--serif); margin: 0; }
  .bulk .actions { display: flex; flex-wrap: wrap; gap: 4px; margin-left: -8px; }

  /* The one bold moment: a small ink chip carries the dragged messages. */
  .dragchip {
    position: fixed; top: -100px; left: 0; max-width: 280px; padding: 4px 10px; border-radius: 4px;
    background: var(--ink); color: var(--paper); font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }

  .toast {
    position: fixed; bottom: 16px; left: 50%; transform: translateX(-50%); z-index: 20;
    display: flex; gap: 12px; align-items: center; max-width: 70vw;
    background: var(--ink); color: var(--paper); padding: 8px 14px; border-radius: 6px;
  }
  .toast span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .toast button { color: var(--paper); text-decoration: underline; }
  .toast button:hover { background: transparent; }
  .toast.error { box-shadow: inset 4px 0 #d93025; padding-left: 18px; } /* the toast inverts the theme, so --warn would be wrong */

  dialog {
    padding: 0; border: 1px solid var(--rule); border-radius: 6px; background: var(--paper); color: var(--ink);
    box-shadow: 0 12px 32px color-mix(in srgb, var(--ink) 18%, transparent);
  }
  dialog::backdrop { background: color-mix(in srgb, var(--ink) 12%, transparent); }
  .ask { width: min(420px, calc(100vw - 32px)); }
  .ask form { display: flex; flex-direction: column; gap: 12px; padding: 20px; }
  .ask p { margin: 0; }
  .buttons { display: flex; justify-content: flex-end; gap: 8px; }
  .buttons button { border: 1px solid var(--rule); }
  .buttons .primary { background: var(--ink); color: var(--paper); border-color: var(--ink); }
  .help { max-height: 85vh; }
  .help > div { padding: 20px 24px; }
  .help h2 { font: 500 22px/1.2 var(--serif); margin: 0 0 12px; }
  .help table { border-collapse: collapse; }
  .help td { padding: 3px 0; vertical-align: top; }
  .help td:first-child { padding-right: 24px; white-space: nowrap; }
  .help p { margin: 12px 0 0; max-width: 46ch; font-size: 13px; }
  kbd { font: inherit; font-weight: 600; }
</style>
