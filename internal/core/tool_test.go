package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fetch runs the HTTPFetch tool against the handler and returns the
// parsed result (or fails the test).
func fetch(t *testing.T, h http.Handler, args HTTPFetchArgs) HTTPFetchResult {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	args.URL = srv.URL + args.URL

	tc := NewToolContext("test", "r1")
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := HTTPFetch.Run(context.Background(), tc, raw)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(HTTPFetchResult)
	if !ok {
		t.Fatalf("unexpected result type %T", out)
	}
	return res
}

func TestHTTPFetchHTMLBecomesText(t *testing.T) {
	page := `<!DOCTYPE html><html><head>
<title>Bloch sphere - Wikipedia</title>
<style>body{color:red}</style>
<script>alert("xss");</script>
</head><body><h1>Bloch sphere</h1>
<p>The state is &alpha;|0&rang; + &beta;|1&rang; with |&alpha;|<sup>2</sup> + |&beta;|<sup>2</sup> = 1.</p>
<ul><li>North pole: |0&rang;</li><li>South pole: |1&rang;</li></ul>
</body></html>`
	res := fetch(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}), HTTPFetchArgs{URL: "/"})

	for _, bad := range []string{"<script", "<style", "<html", "<body", "<h1>", "<li>"} {
		if strings.Contains(res.Body, bad) {
			t.Fatalf("HTML leaked into text: %q present in %q", bad, res.Body)
		}
	}
	for _, want := range []string{"Bloch sphere", "α|0⟩", "β|1⟩", "North pole", "South pole"} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("expected %q in converted text, got %q", want, res.Body)
		}
	}
}

func TestHTTPFetchDefaultCapApplies(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 4096) // 64KB
	res := fetch(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(big))
	}), HTTPFetchArgs{URL: "/"})
	if len(res.Body) > 32<<10 {
		t.Fatalf("default cap exceeded: %d bytes", len(res.Body))
	}
	if !strings.HasPrefix(res.Body, "0123456789abcdef") {
		t.Fatalf("body mangled: %q...", res.Body[:32])
	}
}

func TestHTTPFetchExplicitMaxBytes(t *testing.T) {
	res := fetch(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello world"))
	}), HTTPFetchArgs{URL: "/", MaxBytes: 5})
	if res.Body != "hello" {
		t.Fatalf("body = %q, want %q", res.Body, "hello")
	}
}

func TestHTTPFetchJSONUntouched(t *testing.T) {
	payload := `{"name":"qbit","amp":{"re":0.7071,"im":0}}`
	res := fetch(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(payload))
	}), HTTPFetchArgs{URL: "/"})
	if res.Body != payload {
		t.Fatalf("json body mangled: %q", res.Body)
	}
}

func TestHTTPFetchUnicodeTruncationStaysValid(t *testing.T) {
	// 40 snowmen = 120 bytes; a byte-level cut would split a rune.
	body := strings.Repeat("☃", 40)
	res := fetch(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(body))
	}), HTTPFetchArgs{URL: "/", MaxBytes: 60})
	if got := res.Body; got != strings.Repeat("☃", 20) {
		t.Fatalf("rune-unsafe truncation: %d bytes %q", len(got), got)
	}
}
