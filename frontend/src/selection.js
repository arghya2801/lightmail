// Row clicks toggle while selecting, so a missed checkbox never clears the whole set.
export function selectMessage(items, selected, anchor, current, message, event = {}, checkbox = false) {
  const start = items.findIndex((m) => m.id === (anchor ?? current))
  const end = items.findIndex((m) => m.id === message.id)
  if (event.shiftKey && start >= 0 && end >= 0) {
    for (const m of items.slice(Math.min(start, end), Math.max(start, end) + 1)) selected.add(m.id)
    return { anchor: anchor ?? current, open: false }
  }
  if (checkbox || event.ctrlKey || event.metaKey || selected.size) {
    if (!checkbox && !selected.size && current !== message.id && items.some((m) => m.id === current)) selected.add(current)
    selected.has(message.id) ? selected.delete(message.id) : selected.add(message.id)
    return { anchor: message.id, open: false }
  }
  return { anchor: message.id, open: true }
}

export function adjacentMessage(items, id, key) {
  const i = items.findIndex((m) => m.id === id)
  const next = key === 'Home' ? 0 : key === 'End' ? items.length - 1 : Math.max(0, Math.min(items.length - 1, i + (key === 'ArrowDown' ? 1 : -1)))
  return items[next]
}
