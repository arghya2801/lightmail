// Drag handles for pane and column sizes. Each size is a CSS variable on <html>,
// remembered in localStorage. Double-click a handle to go back to the default.
const root = document.documentElement
let sizes = {}
try {
  sizes = JSON.parse(localStorage.getItem('sizes')) ?? {}
} catch {}
for (const [name, px] of Object.entries(sizes)) root.style.setProperty(name, px + 'px')

function keep() {
  try {
    localStorage.setItem('sizes', JSON.stringify(sizes))
  } catch {}
}

// use:resize={{ name: '--side-w', min, max }} on a handle element.
// The variable sets the size of the handle's parent. y: true resizes a height;
// invert: true grows when dragged left or up.
export function resize(handle, o) {
  const set = (px) => {
    sizes[o.name] = Math.round(Math.min(Math.max(px, o.min), o.max))
    root.style.setProperty(o.name, sizes[o.name] + 'px')
  }
  const current = () => {
    const el = handle.parentElement
    return o.y ? el.offsetHeight : el.offsetWidth
  }
  function down(e) {
    if (e.button !== 0) return
    e.preventDefault()
    handle.setPointerCapture(e.pointerId)
    const start = o.y ? e.clientY : e.clientX
    const from = current()
    const zoom = parseFloat(root.style.zoom) || 1
    handle.classList.add('dragging')
    handle.onpointermove = (m) => set(from + (((o.y ? m.clientY : m.clientX) - start) / zoom) * (o.invert ? -1 : 1))
    handle.onpointerup = () => {
      handle.onpointermove = handle.onpointerup = null
      handle.classList.remove('dragging')
      keep()
    }
  }
  function reset() {
    delete sizes[o.name]
    root.style.removeProperty(o.name)
    keep()
  }
  function key(e) {
    const d = { ArrowLeft: -16, ArrowUp: -16, ArrowRight: 16, ArrowDown: 16 }[e.key]
    if (!d) return
    e.preventDefault()
    e.stopPropagation()
    set(current() + d * (o.invert ? -1 : 1))
    keep()
  }
  handle.role = 'separator'
  handle.tabIndex = 0
  handle.ariaOrientation = o.y ? 'horizontal' : 'vertical'
  handle.ariaLabel = o.label
  handle.addEventListener('pointerdown', down)
  handle.addEventListener('dblclick', reset)
  handle.addEventListener('keydown', key)
  handle.addEventListener('click', (e) => e.stopPropagation())
}
