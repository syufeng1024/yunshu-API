// Package stream implements P18–P25: SSE frame parsing, re-emission, usage
// extraction, first-token timing, heartbeat and abort handling.
//
// The gateway never copies upstream bytes to the client. Every frame is parsed
// into IR and re-encoded; this is what makes usage extraction, first-token
// timing and a uniform error frame possible (AD-05).
package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// MaxFrameBytes is the per-frame ceiling (TDS §4.1 step 1). A larger frame is
// a protocol fault, not something to buffer indefinitely.
const MaxFrameBytes = 4 * 1024 * 1024

// ErrFrameTooLong is returned when a single SSE frame exceeds MaxFrameBytes.
var ErrFrameTooLong = errors.New("stream: SSE frame exceeds 4 MiB")

// Frame is one physical SSE frame.
type Frame struct {
	// Event is the event: field value (possibly empty).
	Event string
	// Data is the concatenated data: payload, with multiple lines joined by \n.
	Data []byte
	// Comment is true for a comment-only block (": keep-alive" and friends).
	Comment bool
	// Raw is the original frame bytes. It is used only for the malformed-frame
	// pass-through required by TDS §4.1 step 4.
	Raw []byte
	// Truncated is true when EOF ended a partial frame.
	Truncated bool
}

// Parser reads physical SSE frames from an upstream body.
type Parser struct {
	br       *bufio.Reader
	maxBytes int
}

// NewParser wraps r. bufio.Reader.ReadBytes reassembles lines across arbitrary
// underlying Read boundaries, which is what makes the half-frame test valid.
func NewParser(r io.Reader) *Parser {
	return &Parser{br: bufio.NewReaderSize(r, 64*1024), maxBytes: MaxFrameBytes}
}

// Next returns the next frame. io.EOF means a clean end of input.
func (p *Parser) Next() (Frame, error) {
	var (
		event   string
		data    [][]byte
		comment bool
		raw     bytes.Buffer
		size    int
	)
	flush := func(truncated bool) (Frame, error) {
		if len(data) == 0 && event == "" && !comment {
			return Frame{}, io.EOF
		}
		joined := bytes.Join(data, []byte("\n"))
		return Frame{Event: event, Data: joined, Comment: comment, Raw: raw.Bytes(), Truncated: truncated}, nil
	}

	for {
		line, err := p.br.ReadBytes('\n')
		if len(line) > 0 {
			size += len(line)
			if size > p.maxBytes {
				return Frame{}, ErrFrameTooLong
			}
			raw.Write(line)
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				// Blank line ends the frame.
				if len(data) == 0 && event == "" && !comment {
					raw.Reset()
					continue
				}
				return flush(false)
			case line[0] == ':':
				comment = true
			case bytes.HasPrefix(line, []byte("event:")):
				event = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
			case bytes.HasPrefix(line, []byte("data:")):
				value := bytes.TrimPrefix(line, []byte("data:"))
				// SSE permits exactly one optional leading space.
				if len(value) > 0 && value[0] == ' ' {
					value = value[1:]
				}
				data = append(data, append([]byte(nil), value...))
			default:
				// id:, retry: and unknown fields are ignored by the relay. They
				// still reset the idle timer because the line arrived.
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				frame, ferr := flush(true)
				if ferr == io.EOF {
					return Frame{}, io.EOF
				}
				return frame, ferr
			}
			return Frame{}, err
		}
	}
}
