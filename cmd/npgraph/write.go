package main

import (
	"bufio"
	"fmt"
	"io"
)

// errWriter is a buffered writer that remembers its first error instead of
// returning one per call.
//
// Both encoders here are long runs of formatted writes with no branch between
// them -- a DOT file is a few thousand lines of the same three statements, an
// SVG a few thousand elements -- and checking each write would bury the
// structure of the document under error handling that can only ever say the
// same thing. Nothing is written after the first failure, and flush reports it.
type errWriter struct {
	w   *bufio.Writer
	err error
}

func newErrWriter(w io.Writer) *errWriter {
	return &errWriter{w: bufio.NewWriter(w)}
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

func (e *errWriter) print(s string) {
	if e.err != nil {
		return
	}
	_, e.err = io.WriteString(e.w, s)
}

func (e *errWriter) flush() error {
	if e.err != nil {
		return e.err
	}
	return e.w.Flush()
}
