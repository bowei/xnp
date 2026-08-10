package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// post drives the UI the way the form does.
func post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	NewUIHandler().ServeHTTP(w, req)
	return w
}

// The landing page has to arrive with the boilerplate params already in the
// textarea: the first thing a user does is edit them, not write them.
func TestUIIndex(t *testing.T) {
	w := httptest.NewRecorder()
	NewUIHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("GET / = %v, want %v", got, want)
	}
	body := w.Body.String()
	for _, want := range []string{"<textarea", "podsPerNamespace", "pods: 1000", `name="seed"`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / body does not contain %q", want)
		}
	}
}

func TestUINotFound(t *testing.T) {
	w := httptest.NewRecorder()
	NewUIHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if got, want := w.Code, http.StatusNotFound; got != want {
		t.Errorf("GET /nope = %v, want %v", got, want)
	}
}

func TestUIGenerate(t *testing.T) {
	w := post(t, "/generate", url.Values{
		"params": {"pods: 20\npodsPerNamespace: {kind: constant, value: 5}\npolicies: 3\n"},
		"seed":   {"7"},
	})

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("POST /generate = %v, want %v\n%s", got, want, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"digraph npgraph",     // the DOT pane
		"kind: NetworkPolicy", // the YAML pane
		"<svg",                // the drawing
		// 20 pods, 4 namespaces of 5, 1 node of 30, 3 policies.
		"28 vertices",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("POST /generate body does not contain %q", want)
		}
	}
	// The edited params have to come back in the textarea, or an edit is lost
	// on every generate.
	if !strings.Contains(body, "pods: 20") {
		t.Errorf("POST /generate did not echo the submitted params back")
	}
}

// A graph past the drawing budget still produces its DOT and YAML; only the
// picture is dropped, with a note saying so.
func TestUIGenerateTooLargeToDraw(t *testing.T) {
	w := post(t, "/generate", url.Values{
		"params": {"pods: 5000\n"},
		"seed":   {"1"},
	})

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("POST /generate = %v, want %v", got, want)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Not drawing the graph") {
		t.Errorf("POST /generate did not explain why the drawing is missing")
	}
	if strings.Contains(body, "<svg") {
		t.Errorf("POST /generate drew a graph past the vertex limit")
	}
	if !strings.Contains(body, "digraph npgraph") {
		t.Errorf("POST /generate dropped the DOT along with the drawing")
	}
}

func TestUIGenerateErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		form    url.Values
		wantErr string
	}{
		{
			name:    "invalid params",
			form:    url.Values{"params": {"pods: -1"}},
			wantErr: "pods must be &gt; 0",
		},
		{
			name:    "unparseable params",
			form:    url.Values{"params": {"pods: ["}},
			wantErr: "parsing params",
		},
		{
			name:    "bad seed",
			form:    url.Values{"params": {"pods: 10"}, "seed": {"not a number"}},
			wantErr: "seed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := post(t, "/generate", tc.form)
			// The error belongs on the page, next to the params that caused it,
			// rather than on an error page the user has to navigate back from.
			if got, want := w.Code, http.StatusOK; got != want {
				t.Fatalf("POST /generate = %v, want %v", got, want)
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.wantErr) {
				t.Errorf("POST /generate body does not mention %q:\n%s", tc.wantErr, body)
			}
			if !strings.Contains(body, "<textarea") {
				t.Error("POST /generate did not render the form back with the error")
			}
		})
	}
}

// An empty textarea means "the defaults", not "an empty params file".
func TestUIGenerateEmptyParams(t *testing.T) {
	w := post(t, "/generate", url.Values{"params": {""}, "seed": {"0"}})
	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("POST /generate = %v, want %v", got, want)
	}
	if !strings.Contains(w.Body.String(), "digraph npgraph") {
		t.Error("POST /generate with empty params produced no graph")
	}
}

func TestUIDownload(t *testing.T) {
	form := url.Values{
		"params": {"pods: 20\npodsPerNamespace: {kind: constant, value: 5}\npolicies: 2\n"},
		"seed":   {"3"},
	}

	for _, tc := range []struct {
		path     string
		contains string
	}{
		{path: "/download/graph.dot", contains: "digraph npgraph"},
		{path: "/download/k8s.yaml", contains: "kind: NetworkPolicy"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := post(t, tc.path, form)
			if got, want := w.Code, http.StatusOK; got != want {
				t.Fatalf("POST %s = %v, want %v", tc.path, got, want)
			}
			if !strings.Contains(w.Body.String(), tc.contains) {
				t.Errorf("POST %s body does not contain %q", tc.path, tc.contains)
			}
			if got, want := w.Header().Get("Content-Disposition"), "attachment"; !strings.Contains(got, want) {
				t.Errorf("Content-Disposition = %q, want it to contain %q", got, want)
			}
		})
	}

	// The same params and seed have to produce the same file here as on the
	// command line, or the UI is a different tool.
	w := post(t, "/download/graph.dot", form)
	params, err := ParseParams([]byte(form.Get("params")))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Generate(params, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := w.Body.String(), formatGraph(g); got != want {
		t.Error("the UI's download differs from what Generate produces for the same params and seed")
	}
}

// A download has nowhere to put an error message, so it answers with a status
// rather than with a page.
func TestUIDownloadErrors(t *testing.T) {
	w := post(t, "/download/graph.dot", url.Values{"params": {"pods: -1"}})
	if got, want := w.Code, http.StatusBadRequest; got != want {
		t.Errorf("POST /download/graph.dot = %v, want %v", got, want)
	}
}
