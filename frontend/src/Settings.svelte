<script module>
  export const zooms = [80, 90, 100, 110, 120, 130, 150]
</script>

<script>
  import { onMount } from 'svelte'
  import { GetConfig, SaveConfig, ServerFolders, ChooseSaveDir } from '../wailsjs/go/main/App'
  import { BrowserOpenURL } from '../wailsjs/runtime/runtime'

  // look is the live appearance, owned by App so keyboard zoom and this page agree.
  // dirty tells App there are unsaved edits, so leaving can ask first.
  let { onsaved, look, onlook, dirty = $bindable(false) } = $props()
  let cfg = $state(null)
  let serverLabels = $state([])
  let filter = $state('')
  let busy = $state(false)
  let error = $state('')
  let saveError = $state('')
  let saved = '' // cfg as last loaded or saved
  $effect(() => {
    dirty = !!cfg && JSON.stringify(cfg) !== saved
  })

  let shown = $derived(
    [...new Set([...(cfg?.labels ?? []), ...serverLabels])]
      .filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
      .sort((a, b) => a.localeCompare(b)),
  )

  onMount(async () => {
    cfg = await GetConfig()
    cfg.labels ??= []
    saved = JSON.stringify(cfg)
  })

  async function loadLabels() {
    busy = true
    error = ''
    try {
      const fs = (await ServerFolders(cfg.user, cfg.password)) ?? []
      serverLabels = fs.map((f) => f.name)
      // First time: tick everything except folders that only repeat others (All Mail, Important).
      if (!cfg.labels.length) cfg.labels = fs.filter((f) => f.default).map((f) => f.name)
    } catch (e) {
      error = String(e)
    }
    busy = false
  }

  function toggle(l) {
    cfg.labels = cfg.labels.includes(l) ? cfg.labels.filter((x) => x !== l) : [...cfg.labels, l]
  }

  async function pickDir() {
    const d = await ChooseSaveDir()
    if (d) cfg.saveDir = d
  }

  async function save(e) {
    e.preventDefault()
    saveError = ''
    try {
      await SaveConfig({ ...cfg, ...look, syncMinutes: +cfg.syncMinutes || 15 })
    } catch (e) {
      saveError = "Couldn't save. " + e
      return
    }
    saved = JSON.stringify(cfg)
    dirty = false
    onsaved()
  }
</script>

{#if cfg}
  <form class="settings" onsubmit={save}>
    <h1>Settings</h1>

    {#if !cfg.password}
      <p>Connect your Gmail account to start. Lightmail syncs the labels you pick.</p>
    {/if}

    <label>Gmail address <input type="email" bind:value={cfg.user} required autocomplete="username" /></label>
    <label>
      App password
      <input type="password" bind:value={cfg.password} required autocomplete="current-password" />
      <span class="hint muted">
        Not your normal password. Create one at
        <a href="https://myaccount.google.com/apppasswords" onclick={(e) => { e.preventDefault(); BrowserOpenURL(e.currentTarget.href) }}>myaccount.google.com/apppasswords</a>
        (needs 2-step verification). IMAP must be on in Gmail settings.
      </span>
    </label>

    <fieldset>
      <legend>Labels to sync</legend>
      <div class="labels-tools">
        <button type="button" onclick={loadLabels} disabled={busy || !cfg.user || !cfg.password}>
          {busy ? 'Loading labels…' : serverLabels.length ? 'Reload labels' : 'Load labels from Gmail'}
        </button>
        {#if serverLabels.length}<input placeholder="Filter labels" bind:value={filter} aria-label="Filter labels" />{/if}
      </div>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="labels">
        {#each shown as l (l)}
          <label class="check"><input type="checkbox" checked={cfg.labels.includes(l)} onchange={() => toggle(l)} /> {l}</label>
        {/each}
      </div>
    </fieldset>

    <label class="check">
      <input type="checkbox" bind:checked={cfg.blockImages} />
      Hide remote images until I ask
    </label>
    <span class="hint muted check-hint">Remote images can tell the sender when you opened a message. When they're hidden, click Load images in a message, or allow a sender for good.</span>

    <label>
      Save folder
      <span class="dir"><input bind:value={cfg.saveDir} /><button type="button" onclick={pickDir}>Choose…</button></span>
      <span class="hint muted">Messages you save as HTML go here.</span>
    </label>
    <label>Check for new mail every <span class="dir"><input class="mins" type="number" min="1" bind:value={cfg.syncMinutes} /> minutes</span></label>

    <fieldset>
      <legend>Appearance</legend>
      <div class="look">
        <span class="muted">Density</span>
        <div class="seg" role="radiogroup" aria-label="Density">
          {#each [['comfortable', 'Comfortable'], ['compact', 'Compact']] as [v, label]}
            <label><input type="radio" name="density" value={v} checked={look.density === v} onchange={() => onlook({ density: v })} /> {label}</label>
          {/each}
        </div>
        <span class="muted">Layout</span>
        <div class="seg" role="radiogroup" aria-label="Layout">
          {#each [['cards', 'Cards'], ['table', 'Table']] as [v, label]}
            <label><input type="radio" name="layout" value={v} checked={look.layout === v} onchange={() => onlook({ layout: v })} /> {label}</label>
          {/each}
        </div>
        <span class="muted">Zoom</span>
        <div class="seg" role="radiogroup" aria-label="Zoom">
          {#each zooms as v}
            <label><input type="radio" name="scale" value={v} checked={look.scale === v} onchange={() => onlook({ scale: v })} /> {v}%</label>
          {/each}
        </div>
      </div>
      <span class="hint muted">Compact fits more mail on screen: narrower panes and tighter rows. Cards lists mail beside the open message; Table shows sender, subject and date in columns above it (the buttons beside the search box switch too). Click a column heading above the list to sort by it; Shift+click adds it as another level (again to reverse it, a third time to remove it). Zoom scales the whole app; Ctrl+Shift+Plus / Minus / 0 work anywhere. Changes apply immediately.</span>
    </fieldset>

    <div>
      <button class="primary" type="submit" disabled={!cfg.labels.length}>Save and sync</button>
      {#if !cfg.labels.length}<span class="hint muted">Pick at least one label to sync.</span>{/if}
      {#if saveError}<p class="error" role="alert">{saveError}</p>{/if}
    </div>
  </form>
{/if}

<style>
  .settings { max-width: 560px; margin: 0 auto; padding: 40px 32px 80px; display: flex; flex-direction: column; gap: 20px; }
  h1 { font: 500 36px/1.1 var(--serif); font-optical-sizing: auto; margin: 0; }
  p { margin: 0; }
  label:not(.check) { display: flex; flex-direction: column; gap: 4px; font-weight: 500; }
  .hint { font-weight: 400; font-size: 13px; }
  .hint a { color: inherit; }
  fieldset { border: 0; padding: 0; margin: 0; }
  legend { font-weight: 500; padding: 0; margin-bottom: 6px; }
  .labels-tools { display: flex; gap: 8px; }
  .labels-tools input { flex: 1; }
  .labels { max-height: 260px; overflow-y: auto; margin-top: 8px; display: flex; flex-direction: column; }
  .look { display: grid; grid-template-columns: auto 1fr; gap: 8px 16px; align-items: center; margin-bottom: 6px; }
  .seg { display: flex; flex-wrap: wrap; gap: 4px; }
  .seg label {
    display: flex; flex-direction: row; align-items: center; gap: 6px; font-weight: 400; cursor: pointer;
    border: 1px solid var(--rule); border-radius: 4px; padding: 4px 10px;
  }
  .seg label:has(input:checked) { border-color: var(--ink); background: var(--paper); }
  .seg label:has(input:focus-visible) { outline: 2px solid var(--focus); outline-offset: 1px; }
  .seg input { position: absolute; opacity: 0; pointer-events: none; }
  .check { display: flex; gap: 8px; align-items: center; padding: 3px 0; }
  .check-hint { margin-top: -16px; }
  .dir { display: flex; gap: 8px; align-items: center; font-weight: 400; }
  .dir input:first-child:not(.mins) { flex: 1; }
  .mins { width: 80px; }
  .error { color: var(--warn); }
  button { border: 1px solid var(--rule); }
  .primary { background: var(--ink); color: var(--paper); border-color: var(--ink); padding: 6px 14px; }
  .primary:hover { background: var(--ink); opacity: 0.9; }
  button:disabled { opacity: 0.5; cursor: default; }
</style>
