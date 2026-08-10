import test from 'node:test'
import assert from 'node:assert/strict'
import { parseSSEBlock } from '../src/api.js'

test('parseSSEBlock parses named JSON events', () => {
  assert.deepEqual(parseSSEBlock('event: citation\ndata: {"number":1,"filename":"demo.md"}'), {
    event: 'citation', data: { number: 1, filename: 'demo.md' },
  })
})

test('parseSSEBlock supports multi-line payloads and ignores comments', () => {
  assert.deepEqual(parseSSEBlock(': keepalive\nevent: message.delta\ndata: {"content":\ndata: "answer"}'), {
    event: 'message.delta', data: { content: 'answer' },
  })
  assert.equal(parseSSEBlock(': keepalive'), null)
})
