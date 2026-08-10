package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRenderSVG(t *testing.T) {
	g := testGraph()

	var buf bytes.Buffer
	if err := RenderSVG(&buf, g, DefaultSVGVertexLimit); err != nil {
		t.Fatalf("RenderSVG() = %v, want nil", err)
	}
	out := buf.String()

	// The document goes straight into an HTML page, so it has to parse. A
	// malformed one takes the rest of the page down with it.
	dec := xml.NewDecoder(strings.NewReader(out))
	var elems, rects, lines int
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the SVG is not well-formed XML: %v\n%s", err, out)
		}
		if start, ok := tok.(xml.StartElement); ok {
			elems++
			switch start.Name.Local {
			case "rect":
				rects++
			case "line":
				lines++
			}
		}
	}
	if elems == 0 {
		t.Fatal("the SVG has no elements")
	}

	// One box per vertex, one line per edge: the picture is of the graph and
	// not of a subset of it.
	if got, want := rects, len(g.Nodes)+len(g.Namespaces)+len(g.Pods)+len(g.Policies); got != want {
		t.Errorf("%d boxes, want %d (one per vertex)", got, want)
	}
	if got, want := lines, countEdges(g); got != want {
		t.Errorf("%d lines, want %d (one per edge)", got, want)
	}
}

// Names are escaped: a fetched graph carries names from the cluster, and the
// SVG is inserted into the UI page unescaped on the strength of this.
func TestRenderSVGEscapes(t *testing.T) {
	g := testGraph()
	g.Pods[0].Name = `<script>alert("x")</script>`
	g.Policies[0].In = []string{g.Pods[0].Name}

	var buf bytes.Buffer
	if err := RenderSVG(&buf, g, DefaultSVGVertexLimit); err != nil {
		t.Fatalf("RenderSVG() = %v, want nil", err)
	}
	if strings.Contains(buf.String(), "<script>") {
		t.Errorf("RenderSVG() emitted an unescaped name:\n%s", buf.String())
	}
}

// A picture of ten thousand pods is not a picture of anything, and drawing it
// costs the browser more than the reader gets back.
func TestRenderSVGTooLarge(t *testing.T) {
	g := testGraph()

	var buf bytes.Buffer
	err := RenderSVG(&buf, g, 3)
	if !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("RenderSVG(limit=3) = %v, want ErrGraphTooLarge", err)
	}
	if !strings.Contains(err.Error(), "limit 3") {
		t.Errorf("RenderSVG() = %q, want it to name the limit", err)
	}

	// A limit of zero is no limit, for a caller that wants the whole picture.
	buf.Reset()
	if err := RenderSVG(&buf, g, 0); err != nil {
		t.Errorf("RenderSVG(limit=0) = %v, want nil", err)
	}
}

func TestRenderSVGEmptyGraph(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderSVG(&buf, &Graph{}, DefaultSVGVertexLimit); err != nil {
		t.Fatalf("RenderSVG(empty) = %v, want nil", err)
	}
	if !strings.Contains(buf.String(), "</svg>") {
		t.Errorf("RenderSVG(empty) produced no document:\n%s", buf.String())
	}
}

func TestCountEdges(t *testing.T) {
	g := testGraph()
	// Four pods with a node and a namespace each, plus one In and one Out edge.
	if got, want := countEdges(g), 4*2+2; got != want {
		t.Errorf("countEdges() = %v, want %v", got, want)
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{in: "short", n: 10, want: "short"},
		{in: "exactly-10", n: 10, want: "exactly-10"},
		{in: "far-too-long-for-this", n: 10, want: "far-too-l…"},
		{in: "x", n: 1, want: "x"},
		{in: "xy", n: 1, want: "x"},
	} {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
