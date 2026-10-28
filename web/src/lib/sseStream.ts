/**
 * Incremental Server-Sent Events parser.
 *
 * The network divides a single event across chunks arbitrarily, so the parser
 * keeps the unconsumed tail in an internal buffer and only emits an event once
 * its terminating blank line has arrived. Splitting each chunk on "\n\n" at the
 * call site loses the half-frame that straddles the boundary.
 *
 * Line terminators follow the SSE spec: CRLF, LF, or a lone CR. A trailing CR
 * is held back until the next chunk, because it may be the first half of CRLF.
 */

export interface SSEEvent {
  /** Value of the last `event:` line, or "message" when absent. */
  event: string
  /** `data:` lines joined with "\n"; the spec's trailing newline is removed. */
  data: string
  /** True when data is the OpenAI-compatible stream terminator "[DONE]". */
  done: boolean
}

export interface SSEParser {
  /** Feeds a decoded string chunk and returns every event completed by it. */
  push(chunk: string): SSEEvent[]
  /** Flushes a final partial line/event at end of stream. */
  flush(): SSEEvent[]
}

export function createSSEParser(): SSEParser {
  let buffer = ''
  let dataLines: string[] = []
  let eventName = ''

  const dispatch = (out: SSEEvent[]) => {
    if (dataLines.length === 0) {
      // Per the spec, an empty data buffer aborts the event being built.
      eventName = ''
      return
    }
    const data = dataLines.join('\n')
    out.push({
      event: eventName || 'message',
      data,
      done: data.trim() === '[DONE]',
    })
    dataLines = []
    eventName = ''
  }

  const consumeLine = (line: string, out: SSEEvent[]) => {
    if (line === '') {
      dispatch(out)
      return
    }
    if (line.startsWith(':')) {
      // Comment / keep-alive line.
      return
    }
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    // A single space after the colon is part of the framing, not the value.
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'data') {
      dataLines.push(value)
    } else if (field === 'event') {
      eventName = value
    }
  }

  const drain = (final: boolean, out: SSEEvent[]) => {
    for (;;) {
      const lf = buffer.indexOf('\n')
      if (lf === -1) {
        const cr = buffer.indexOf('\r')
        if (cr === -1) return
        if (cr < buffer.length - 1) {
          const line = buffer.slice(0, cr)
          buffer = buffer.slice(cr + 1)
          if (buffer.startsWith('\n')) buffer = buffer.slice(1)
          consumeLine(line, out)
          continue
        }
        if (final) {
          const line = buffer.slice(0, cr)
          buffer = ''
          consumeLine(line, out)
          continue
        }
        // Lone CR at the very end: wait to see whether a LF follows.
        return
      }
      let line = buffer.slice(0, lf)
      buffer = buffer.slice(lf + 1)
      if (line.endsWith('\r')) line = line.slice(0, -1)
      consumeLine(line, out)
    }
  }

  return {
    push(chunk) {
      const out: SSEEvent[] = []
      if (chunk !== '') buffer += chunk
      drain(false, out)
      return out
    },
    flush() {
      const out: SSEEvent[] = []
      if (buffer !== '') {
        let line = buffer
        buffer = ''
        if (line.endsWith('\r')) line = line.slice(0, -1)
        consumeLine(line, out)
      }
      dispatch(out)
      return out
    },
  }
}
