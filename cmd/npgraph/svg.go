package main

import (
	"errors"
	"fmt"
	"html"
	"io"
)

// The SVG is drawn here rather than by shelling out to graphviz, because the UI
// has to work on a machine that does not have graphviz installed and the layout
// a resource graph wants is not one a general-purpose layout engine would find:
// the four kinds are four columns, and what a reader is looking for is how far
// across the columns a policy reaches.

// ErrGraphTooLarge is returned by RenderSVG for a graph past the vertex budget.
// A picture of ten thousand pods is not a picture of anything, and drawing it
// costs the browser more than the user gets back, so the caller is told to show
// something else instead of being handed an unreadable page.
var ErrGraphTooLarge = errors.New("graph is too large to draw")

// DefaultSVGVertexLimit is the number of vertices past which RenderSVG gives
// up. It is a display budget rather than a property of the graph.
const DefaultSVGVertexLimit = 600

const (
	svgColWidth  = 190
	svgBoxWidth  = 150
	svgBoxHeight = 20
	svgMargin    = 30
	svgRowHeight = 26
)

type svgColumn struct {
	title string
	names []string
	class string
}

// RenderSVG draws g as four columns -- nodes, namespaces, pods, policies --
// with the placement and policy edges between them.
func RenderSVG(w io.Writer, g *Graph, maxVertices int) error {
	total := len(g.Nodes) + len(g.Namespaces) + len(g.Pods) + len(g.Policies)
	if maxVertices > 0 && total > maxVertices {
		return fmt.Errorf("%w: %d vertices, limit %d", ErrGraphTooLarge, total, maxVertices)
	}

	cols := []svgColumn{
		{title: "Nodes", class: "node"},
		{title: "Namespaces", class: "ns"},
		{title: "Pods", class: "pod"},
		{title: "Policies", class: "np"},
	}
	for _, n := range g.Nodes {
		cols[0].names = append(cols[0].names, n.Name)
	}
	for _, ns := range g.Namespaces {
		cols[1].names = append(cols[1].names, ns.Name)
	}
	for _, p := range g.Pods {
		cols[2].names = append(cols[2].names, p.Name)
	}
	for _, p := range g.Policies {
		cols[3].names = append(cols[3].names, p.Name)
	}

	tallest := 1
	for _, c := range cols {
		if len(c.names) > tallest {
			tallest = len(c.names)
		}
	}
	height := 2*svgMargin + tallest*svgRowHeight
	width := 2*svgMargin + len(cols)*svgColWidth

	// Every column is spread over the same vertical span, so a column of six
	// namespaces lines up with a column of six hundred pods instead of hugging
	// the top of the picture.
	pos := map[string][2]float64{}
	for ci, c := range cols {
		x := float64(svgMargin + ci*svgColWidth)
		span := float64(height - 2*svgMargin)
		for i, name := range c.names {
			y := float64(svgMargin) + span*(float64(i)+0.5)/float64(len(c.names))
			pos[name] = [2]float64{x, y}
		}
	}

	bw := newErrWriter(w)
	bw.printf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" font-family="sans-serif" font-size="10">`+"\n", width, height, width, height)
	bw.print(`<defs><marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0,0 L8,4 L0,8 z" fill="currentColor"/></marker></defs>` + "\n")
	bw.print(`<style>
.edge { stroke-width: 1; fill: none; opacity: 0.45; }
.edge-place { color: #888; stroke: #888; }
.edge-in { color: #1a7f37; stroke: #1a7f37; }
.edge-out { color: #9a3412; stroke: #9a3412; }
.box { stroke: #333; stroke-width: 1; }
.box-node { fill: #eef2ff; }
.box-ns { fill: #ecfdf5; }
.box-pod { fill: #f8fafc; }
.box-np { fill: #fef3c7; }
.title { font-size: 12px; font-weight: bold; fill: #111; }
.label { fill: #111; }
</style>` + "\n")

	// Edges first, so the boxes are drawn over them and the text stays legible.
	edge := func(from, to, class string) {
		a, ok := pos[from]
		if !ok {
			return
		}
		b, ok := pos[to]
		if !ok {
			return
		}
		// The line runs between the facing sides of the two boxes, whichever
		// way round the columns are: a policy's Out edge goes right to left.
		x1, x2 := a[0]+svgBoxWidth, b[0]
		if b[0] < a[0] {
			x1, x2 = a[0], b[0]+svgBoxWidth
		}
		bw.printf(`<line class="edge %s" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" marker-end="url(#arrow)"/>`+"\n",
			class, x1, a[1], x2, b[1])
	}

	for _, p := range g.Pods {
		edge(p.Node, p.Name, "edge-place")
		edge(p.Namespace, p.Name, "edge-place")
	}
	for _, p := range g.Policies {
		for _, in := range p.In {
			edge(in, p.Name, "edge-in")
		}
		for _, out := range p.Out {
			edge(p.Name, out, "edge-out")
		}
		for _, ns := range p.OutNamespaces {
			edge(p.Name, ns, "edge-out")
		}
	}

	for ci, c := range cols {
		x := svgMargin + ci*svgColWidth
		bw.printf(`<text class="title" x="%d" y="%d">%s (%d)</text>`+"\n",
			x, svgMargin-12, html.EscapeString(c.title), len(c.names))
		for _, name := range c.names {
			p := pos[name]
			bw.printf(`<rect class="box box-%s" x="%.1f" y="%.1f" width="%d" height="%d" rx="3"/>`+"\n",
				c.class, p[0], p[1]-svgBoxHeight/2, svgBoxWidth, svgBoxHeight)
			bw.printf(`<text class="label" x="%.1f" y="%.1f">%s</text>`+"\n",
				p[0]+5, p[1]+4, html.EscapeString(truncate(name, 22)))
		}
	}

	bw.print("</svg>\n")
	return bw.flush()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
