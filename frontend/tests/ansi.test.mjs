import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseAnsi } from '../src/utils/ansi.ts'

test('renders tracing colours, dim timestamps and italic fields with resets', () => {
  assert.deepEqual(parseAnsi('\x1b[2mtime\x1b[0m \x1b[32m INFO\x1b[0m \x1b[3mcall_id\x1b[0m=123'), [
    { text: 'time', style: { opacity: '0.6' } },
    { text: ' ', style: {} },
    { text: ' INFO', style: { color: '#00cd00' } },
    { text: ' ', style: {} },
    { text: 'call_id', style: { fontStyle: 'italic' } },
    { text: '=123', style: {} },
  ])
})

test('supports bright, indexed and truecolour output and selective resets', () => {
  assert.deepEqual(parseAnsi('\x1b[1;91;48;5;232ma\x1b[22;39;49mb\x1b[38;2;1;2;3mc'), [
    { text: 'a', style: { fontWeight: 'bold', color: '#ff0000', backgroundColor: 'rgb(8,8,8)' } },
    { text: 'b', style: {} },
    { text: 'c', style: { color: 'rgb(1,2,3)' } },
  ])
})

test('keeps untrusted markup as text and discards non-style terminal controls', () => {
  assert.deepEqual(parseAnsi('\x1b]8;;https://example.com\x07<script>\x1b]8;;\x1b\\\x1b[2K\x1b[999m'), [
    { text: '<script>', style: {} },
  ])
  assert.deepEqual(parseAnsi('plain\ntext'), [{ text: 'plain\ntext', style: {} }])
  assert.deepEqual(parseAnsi('\x1b[38;2;999;0;0mtext'), [{ text: 'text', style: {} }])
})
