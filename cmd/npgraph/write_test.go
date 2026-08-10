package main

import (
	"errors"
	"testing"
)

// failWriter fails after letting n bytes through, so a test can put the failure
// in the middle of a document rather than at its first byte.
type failWriter struct {
	remaining int
}

var errWriteFailed = errors.New("write failed")

func (f *failWriter) Write(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, errWriteFailed
	}
	if len(p) > f.remaining {
		n := f.remaining
		f.remaining = 0
		return n, errWriteFailed
	}
	f.remaining -= len(p)
	return len(p), nil
}

// The encoders write hundreds of lines without checking one at a time, so the
// error has to survive to the flush -- otherwise a truncated file would be
// reported as a success.
func TestErrWriterReportsTheFirstError(t *testing.T) {
	w := newErrWriter(&failWriter{})
	w.err = errWriteFailed

	// Nothing is written once the error is set, and flush hands it back rather
	// than the (successful) flush of an empty buffer.
	w.printf("more %s", "output")
	w.print("and more")
	if got := w.flush(); !errors.Is(got, errWriteFailed) {
		t.Errorf("flush() = %v, want %v", got, errWriteFailed)
	}
}

func TestWriteDOTReportsWriteErrors(t *testing.T) {
	g := testGraph()
	// Small enough that the failure lands mid-document, past the bufio buffer's
	// first flush.
	if err := WriteDOT(&failWriter{remaining: 8}, g); !errors.Is(err, errWriteFailed) {
		t.Errorf("WriteDOT() = %v, want %v", err, errWriteFailed)
	}
	if err := WriteDOT(&failWriter{}, g); !errors.Is(err, errWriteFailed) {
		t.Errorf("WriteDOT() = %v, want %v", err, errWriteFailed)
	}
}

func TestRenderSVGReportsWriteErrors(t *testing.T) {
	if err := RenderSVG(&failWriter{}, testGraph(), DefaultSVGVertexLimit); !errors.Is(err, errWriteFailed) {
		t.Errorf("RenderSVG() = %v, want %v", err, errWriteFailed)
	}
}

func TestWriteK8sYAMLReportsWriteErrors(t *testing.T) {
	objs, err := BuildObjects(testGraph())
	if err != nil {
		t.Fatalf("BuildObjects() = %v, want nil", err)
	}
	if err := WriteK8sYAML(&failWriter{}, objs); !errors.Is(err, errWriteFailed) {
		t.Errorf("WriteK8sYAML() = %v, want %v", err, errWriteFailed)
	}
}
