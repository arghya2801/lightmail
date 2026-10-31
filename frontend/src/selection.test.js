import { test } from 'node:test'
import assert from 'node:assert/strict'
import { selectMessage, adjacentMessage } from './selection.js'

const items = [1, 2, 3, 4].map((id) => ({ id }))

test('opening a row establishes a range anchor', () => {
  const selected = new Set()
  const first = selectMessage(items, selected, null, null, items[0])
  assert.equal(first.open, true)
  selectMessage(items, selected, first.anchor, 1, items[2], { shiftKey: true })
  assert.deepEqual([...selected], [1, 2, 3])
})

test('checkbox Shift-click selects a range in either direction', () => {
  const selected = new Set()
  const first = selectMessage(items, selected, null, null, items[2], {}, true)
  selectMessage(items, selected, first.anchor, null, items[0], { shiftKey: true }, true)
  assert.deepEqual([...selected].sort(), [1, 2, 3])
})

test('missing a checkbox preserves the other selected messages', () => {
  const selected = new Set([1, 2])
  assert.equal(selectMessage(items, selected, 2, 1, items[2]).open, false)
  assert.deepEqual([...selected], [1, 2, 3])
  selectMessage(items, selected, 3, 1, items[1])
  assert.deepEqual([...selected], [1, 3])
})

test('a missing anchor does not select an unrelated range', () => {
  const selected = new Set()
  selectMessage(items, selected, 99, null, items[1], { shiftKey: true }, true)
  assert.deepEqual([...selected], [2])
})

test('Ctrl-click includes the already open message', () => {
  const selected = new Set()
  selectMessage(items, selected, 1, 1, items[2], { ctrlKey: true })
  assert.deepEqual([...selected], [1, 3])
})

test('Ctrl-click selects the open row and excludes an open message outside the list', () => {
  const selected = new Set()
  selectMessage(items, selected, 1, 1, items[0], { ctrlKey: true })
  assert.deepEqual([...selected], [1])
  selected.clear()
  selectMessage(items, selected, null, 99, items[1], { ctrlKey: true })
  assert.deepEqual([...selected], [2])
})

test('arrow navigation and Home/End stay inside the loaded rows', () => {
  assert.equal(adjacentMessage(items, 2, 'ArrowDown').id, 3)
  assert.equal(adjacentMessage(items, 2, 'ArrowUp').id, 1)
  assert.equal(adjacentMessage(items, 1, 'ArrowUp').id, 1)
  assert.equal(adjacentMessage(items, 4, 'ArrowDown').id, 4)
  assert.equal(adjacentMessage(items, 2, 'Home').id, 1)
  assert.equal(adjacentMessage(items, 2, 'End').id, 4)
  assert.equal(adjacentMessage([], null, 'ArrowDown'), undefined)
})

test('Shift+arrow can establish a range from a focused unopened row', () => {
  const selected = new Set()
  const next = adjacentMessage(items, 2, 'ArrowDown')
  const start = selectMessage(items, selected, null, null, items[1], {}, true)
  selectMessage(items, selected, start.anchor, null, next, { shiftKey: true })
  assert.deepEqual([...selected], [2, 3])
})
