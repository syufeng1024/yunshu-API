import { describe, expect, it } from 'vitest'
import { createSSEParser } from './sseStream'

describe('createSSEParser', () => {
  it('parses a single complete frame', () => {
    const parser = createSSEParser()
    const events = parser.push('data: hello\n\n')
    expect(events).toEqual([{ event: 'message', data: 'hello', done: false }])
  })

  it('parses several frames delivered in one chunk', () => {
    const parser = createSSEParser()
    const events = parser.push('data: one\n\ndata: two\n\n')
    expect(events.map((event) => event.data)).toEqual(['one', 'two'])
  })

  it('keeps half a frame buffered across three chunks', () => {
    const parser = createSSEParser()
    expect(parser.push('data: {"choices":[')).toEqual([])
    expect(parser.push('{"delta":{"content":"你好"}}')).toEqual([])
    expect(parser.push(']}\n\n')).toEqual([
      {
        event: 'message',
        data: '{"choices":[{"delta":{"content":"你好"}}]}',
        done: false,
      },
    ])
  })

  it('stitches data split across an arbitrary number of chunks', () => {
    const parser = createSSEParser()
    const chunks = ['da', 'ta: par', 'tial', '\n', '\n']
    const events = chunks.flatMap((chunk) => parser.push(chunk))
    expect(events).toEqual([{ event: 'message', data: 'partial', done: false }])
  })

  it('joins multiple data lines with a newline', () => {
    const parser = createSSEParser()
    const events = parser.push('data: line one\ndata: line two\n\n')
    expect(events[0].data).toBe('line one\nline two')
  })

  it('ignores comment / keep-alive lines', () => {
    const parser = createSSEParser()
    const events = parser.push(': keep-alive\ndata: real\n\n: another comment\n\n')
    expect(events).toHaveLength(1)
    expect(events[0].data).toBe('real')
  })

  it('marks [DONE] as the terminating event', () => {
    const parser = createSSEParser()
    const events = parser.push('data: {"ok":true}\n\ndata: [DONE]\n\n')
    expect(events).toHaveLength(2)
    expect(events[1]).toMatchObject({ data: '[DONE]', done: true })
  })

  it('ignores empty chunks and accepts the optional space after data:', () => {
    const parser = createSSEParser()
    expect(parser.push('')).toEqual([])
    expect(parser.push('data:nospace\n\n')).toEqual([
      { event: 'message', data: 'nospace', done: false },
    ])
  })

  it('handles CRLF split across chunks', () => {
    const parser = createSSEParser()
    expect(parser.push('data: crlf\r')).toEqual([])
    const events = parser.push('\n\r\n')
    expect(events).toEqual([{ event: 'message', data: 'crlf', done: false }])
  })

  it('preserves `event:` names', () => {
    const parser = createSSEParser()
    const events = parser.push('event: error\ndata: {"error":{}}\n\n')
    expect(events[0]).toMatchObject({ event: 'error', done: false })
  })

  it('flushes a final frame that has no trailing blank line', () => {
    const parser = createSSEParser()
    expect(parser.push('data: tail')).toEqual([])
    expect(parser.flush()).toEqual([{ event: 'message', data: 'tail', done: false }])
  })

  it('keeps multi-byte UTF-8 intact when bytes are split across chunks', () => {
    // "你好" is 6 bytes; the second character is sliced in the middle.
    const bytes = new TextEncoder().encode('data: 你好\n\n')
    const cut = bytes.length - 4
    const decoder = new TextDecoder()
    const parser = createSSEParser()
    const events: string[] = []

    for (const part of [bytes.slice(0, cut), bytes.slice(cut)]) {
      const text = decoder.decode(part, { stream: true })
      for (const event of parser.push(text)) events.push(event.data)
    }
    for (const event of parser.push(decoder.decode())) events.push(event.data)
    for (const event of parser.flush()) events.push(event.data)

    expect(events).toEqual(['你好'])
  })
})
