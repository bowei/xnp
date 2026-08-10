package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
)

// The UI is server-rendered and has no assets: one HTML template, one form,
// three panes. A tool that exists to be pointed at experiments should start
// with `go run` and no build step, and there is nothing here a form post cannot
// express -- the expensive part is generating the graph, which is a round trip
// either way.

type uiResult struct {
	Params string
	Seed   uint64
	Error  string

	SVG      template.HTML
	SVGNote  string
	DOT      string
	YAML     string
	Vertices int
	Edges    int
}

var uiTemplate = template.Must(template.New("ui").Parse(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>npgraph</title>
<style>
body { font-family: sans-serif; margin: 1rem; color: #111; }
h1 { font-size: 1.2rem; }
form { display: flex; gap: 1rem; align-items: flex-start; flex-wrap: wrap; }
textarea { font-family: monospace; font-size: 12px; width: 28rem; height: 24rem; }
pre { background: #f6f8fa; padding: 0.5rem; overflow: auto; max-height: 24rem; font-size: 12px; }
.err { color: #b00; font-weight: bold; }
.panes { display: flex; gap: 1rem; align-items: flex-start; flex-wrap: wrap; }
.pane { flex: 1 1 28rem; min-width: 24rem; }
.graph { overflow: auto; border: 1px solid #ddd; max-height: 40rem; }
.controls { display: flex; flex-direction: column; gap: 0.5rem; }
</style>
</head>
<body>
<h1>npgraph</h1>

<form method="POST" action="/generate">
  <textarea name="params" spellcheck="false">{{.Params}}</textarea>
  <div class="controls">
    <label>seed <input type="number" name="seed" value="{{.Seed}}" min="0"></label>
    <button type="submit">Generate</button>
    <button type="submit" formaction="/download/graph.dot">Download graph.dot</button>
    <button type="submit" formaction="/download/k8s.yaml">Download k8s.yaml</button>
    {{if .Vertices}}<p>{{.Vertices}} vertices, {{.Edges}} edges</p>{{end}}
  </div>
</form>

{{if .Error}}<p class="err">{{.Error}}</p>{{end}}

{{if .SVGNote}}<p>{{.SVGNote}}</p>{{end}}
{{if .SVG}}<div class="graph">{{.SVG}}</div>{{end}}

<div class="panes">
{{if .DOT}}<div class="pane"><h2>graph.dot</h2><pre>{{.DOT}}</pre></div>{{end}}
{{if .YAML}}<div class="pane"><h2>k8s.yaml</h2><pre>{{.YAML}}</pre></div>{{end}}
</div>
</body>
</html>
`))

// NewUIHandler builds the UI's routes. It is a handler rather than a server so
// that a test can drive it through httptest without binding a port.
func NewUIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/generate", handleGenerate)
	mux.HandleFunc("/download/graph.dot", handleDownload)
	mux.HandleFunc("/download/k8s.yaml", handleDownload)
	return mux
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	var buf bytes.Buffer
	if err := WriteParams(&buf, DefaultParams()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderUI(w, &uiResult{Params: buf.String()})
}

// formInput reads the two things the form carries. The params text is echoed
// back on the result page whether or not it parsed, so a user editing a
// distribution into an invalid shape gets their edit back with the error rather
// than the defaults and a lost change.
func formInput(r *http.Request) (params string, seed uint64, p *Params, err error) {
	if err := r.ParseForm(); err != nil {
		return "", 0, nil, err
	}
	params = r.FormValue("params")
	if s := r.FormValue("seed"); s != "" {
		seed, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			return params, 0, nil, fmt.Errorf("seed: %w", err)
		}
	}
	if params == "" {
		return params, seed, DefaultParams(), nil
	}
	p, err = ParseParams([]byte(params))
	return params, seed, p, err
}

func handleGenerate(w http.ResponseWriter, r *http.Request) {
	paramsText, seed, params, err := formInput(r)
	res := &uiResult{Params: paramsText, Seed: seed}
	if err != nil {
		res.Error = err.Error()
		renderUI(w, res)
		return
	}

	g, err := Generate(params, seed)
	if err != nil {
		res.Error = err.Error()
		renderUI(w, res)
		return
	}

	var dot bytes.Buffer
	if err := WriteDOT(&dot, g); err != nil {
		res.Error = err.Error()
		renderUI(w, res)
		return
	}
	res.DOT = dot.String()

	objs, err := BuildObjects(g)
	if err != nil {
		res.Error = err.Error()
		renderUI(w, res)
		return
	}
	var yamlBuf bytes.Buffer
	if err := WriteK8sYAML(&yamlBuf, objs); err != nil {
		res.Error = err.Error()
		renderUI(w, res)
		return
	}
	res.YAML = yamlBuf.String()

	res.Vertices = len(g.Nodes) + len(g.Namespaces) + len(g.Pods) + len(g.Policies)
	res.Edges = countEdges(g)

	var svg bytes.Buffer
	if err := RenderSVG(&svg, g, DefaultSVGVertexLimit); err != nil {
		res.SVGNote = "Not drawing the graph: " + err.Error() + ". The DOT and YAML below are complete."
	} else {
		// The markup comes from RenderSVG, which escapes every name it puts in
		// the document, so it is inserted unescaped on purpose.
		res.SVG = template.HTML(svg.String()) //nolint:gosec // G203: not user-controlled markup.
	}

	renderUI(w, res)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	_, seed, params, err := formInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g, err := Generate(params, seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var buf bytes.Buffer
	switch r.URL.Path {
	case "/download/graph.dot":
		err = WriteDOT(&buf, g)
	case "/download/k8s.yaml":
		var objs *Objects
		if objs, err = BuildObjects(g); err == nil {
			err = WriteK8sYAML(&buf, objs)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	name := r.URL.Path[len("/download/"):]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	_, _ = w.Write(buf.Bytes())
}

func renderUI(w http.ResponseWriter, res *uiResult) {
	var buf bytes.Buffer
	if err := uiTemplate.Execute(&buf, res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// countEdges counts the relationships the graph asserts, which is the number
// the size of a rendering problem scales with -- not the vertex count, since a
// single policy can carry thousands of edges.
func countEdges(g *Graph) int {
	n := 2 * len(g.Pods) // node -> pod, namespace -> pod
	for _, p := range g.Policies {
		n += len(p.In) + len(p.Out) + len(p.OutNamespaces)
	}
	return n
}
