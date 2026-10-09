<script>
  // A filterable folder list for Move to, Add label and Go to. Enter picks the highlighted
  // one; Tab steps onto the list, where Space picks, so one hand on the left side is enough.
  let { title, folders, onpick, onclose } = $props()
  let filter = $state('')
  let at = $state(0)
  let input = $state()
  $effect(() => input.focus())
  let shown = $derived(folders.filter((f) => f.label.toLowerCase().includes(filter.toLowerCase())))
  $effect(() => {
    filter
    at = 0
  })

  function onkeydown(e) {
    e.stopPropagation() // keep j/k/e from reaching the app while typing
    if (e.key === 'Escape') onclose()
    else if (e.key === 'Tab') {
      // Cycle between the filter and the folders, never out to the app behind.
      const stops = [input, ...e.currentTarget.querySelectorAll('li button')]
      stops[(stops.indexOf(e.target) + (e.shiftKey ? -1 : 1) + stops.length) % stops.length].focus()
    } else if (e.target !== input) return // Space and Enter press the focused folder natively
    else if (e.key === 'ArrowDown') at = Math.min(at + 1, shown.length - 1)
    else if (e.key === 'ArrowUp') at = Math.max(at - 1, 0)
    else if (e.key === 'Enter' && shown[at]) onpick(shown[at].name)
    else return
    e.preventDefault()
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="veil" onclick={onclose}>
  <div class="picker" role="dialog" aria-label={title} tabindex="-1" onclick={(e) => e.stopPropagation()} {onkeydown}>
    <input bind:this={input} bind:value={filter} placeholder={title} aria-label={title} />
    <ul role="listbox" aria-label="Folders">
      {#each shown as f, i (f.name)}
        <li role="option" aria-selected={i === at}>
          <button class:at={i === at} onclick={() => onpick(f.name)} onmouseenter={() => (at = i)} onfocus={() => (at = i)}>{f.label}</button>
        </li>
      {:else}
        <li class="muted none">No folder matches.</li>
      {/each}
    </ul>
  </div>
</div>

<style>
  .veil { position: fixed; inset: 0; z-index: 10; display: flex; justify-content: center; align-items: flex-start; padding-top: 12vh; }
  .picker {
    width: min(420px, calc(100vw - 32px)); background: var(--paper); border: 1px solid var(--rule); border-radius: 6px;
    box-shadow: 0 12px 32px color-mix(in srgb, var(--ink) 18%, transparent); display: flex; flex-direction: column;
  }
  input { margin: 8px; }
  ul { list-style: none; margin: 0; padding: 0 0 6px; max-height: 50vh; overflow-y: auto; }
  button { display: block; width: 100%; text-align: left; border-radius: 0; padding: 5px 14px; }
  button.at { background: var(--picked); }
  .none { padding: 6px 14px; }
</style>
