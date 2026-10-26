package stream

import (
	"io"
	"strings"
	"testing"
)

// chunkReader returns the supplied chunks across separate Read calls, which is
// what a half-frame test needs.
type chunkReader struct {
	chunks [][]byte
	idx    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	r.chunks[r.idx] = r.chunks[r.idx][n:]
	if len(r.chunks[r.idx]) == 0 {
		r.idx++
	}
	return n, nil
}

func (r *chunkReader) Close() error { return nil }

// A7: "data: {"cho" and "ices":[]}" fed as two reads become one frame.
func TestA7HalfFrameIsReassembled(t *testing.T) {
	r := &chunkReader{chunks: [][]byte{
		[]byte(`data: {"cho`),
		[]byte(`ices":[]}` + "\n\n"),
	}}
	p := NewParser(r)
	frame, err := p.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if string(frame.Data) != `{"choices":[]}` {
		t.Fatalf("data = %q", frame.Data)
	}
	if _, err := p.Next(); err != io.EOF {
		t.Fatalf("second next = %v, want EOF", err)
	}
}

func TestParserHandlesCommentsEventsAndMultipleDataLines(t *testing.T) {
	body := ": comment\n\n" +
		"event: chunk\n" +
		"data: line1\n" +
		"data: line2\n\n" +
		"id: 7\nretry: 100\n\n"
	p := NewParser(strings.NewReader(body))
	comment, err := p.Next()
	if err != nil || !comment.Comment {
		t.Fatalf("comment frame = %+v, %v", comment, err)
	}
	frame, err := p.Next()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if frame.Event != "chunk" || string(frame.Data) != "line1\nline2" {
		t.Fatalf("frame = %+v (data %q)", frame, frame.Data)
	}
	// id:/retry: carry no data or event, so the block is not a dispatchable
	// frame; the parser skips it.
	if _, err := p.Next(); err != io.EOF {
		t.Fatalf("third next = %v, want EOF", err)
	}
}

func TestParserReportsTruncatedResidual(t *testing.T) {
	p := NewParser(strings.NewReader("data: {\"partial\":1}"))
	frame, err := p.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if !frame.Truncated || string(frame.Data) != `{"partial":1}` {
		t.Fatalf("frame = %+v", frame)
	}
}

func TestParserRejectsOversizedFrame(t *testing.T) {
	p := NewParser(strings.NewReader("data: " + strings.Repeat("x", MaxFrameBytes) + "\n\n"))
	if _, err := p.Next(); err != ErrFrameTooLong {
		t.Fatalf("err = %v, want ErrFrameTooLong", err)
	}
}
