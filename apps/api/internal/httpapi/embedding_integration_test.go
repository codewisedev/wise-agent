package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// conceptEmbedder is a stand-in /embeddings server: each dimension is one concept.
func conceptEmbedder(t *testing.T, calls *atomic.Int64) *httptest.Server {
	concepts := [][]string{{"retr", "backoff", "again"}, {"gateway", "provider"}, {"board", "column", "list"}, {"token", "secret"}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var req struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data  []item         `json:"data"`
			Usage map[string]int `json:"usage"`
		}{Usage: map[string]int{"prompt_tokens": 10 * len(req.Input)}}
		dims := req.Dimensions
		if req.Model == "fixed-32" { // ignores the requested size
			dims = 32
		}
		for i, text := range req.Input {
			v := make([]float32, dims)
			v[dims-1] = 0.05 // never all-zero
			for c, words := range concepts {
				for _, wd := range words {
					if strings.Contains(strings.ToLower(text), wd) {
						v[c] = 1
					}
				}
			}
			out.Data = append(out.Data, item{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func setEmbedding(t *testing.T, e *env, emb map[string]any) {
	t.Helper()
	var models map[string]any
	var version float64
	for _, it := range e.expect(e.do("GET", "/settings", nil), 200)["items"].([]any) {
		if s := it.(map[string]any); s["key"] == "models" {
			models, version = s["value"].(map[string]any), s["version"].(float64)
		}
	}
	models["embedding"] = emb
	e.expect(e.do("PUT", "/settings/models", map[string]any{"value": models, "version": version}), 200)
}

func TestSemanticSearchWithEmbeddings(t *testing.T) {
	e := setup(t)
	e.login()
	var calls atomic.Int64
	srv := conceptEmbedder(t, &calls)
	defer srv.Close()

	// Off by default: keyword search only, status says why.
	if st := e.expect(e.do("GET", "/context/embeddings", nil), 200); st["configured"] != false || st["problem"] == nil {
		t.Fatalf("status off: %v", st)
	}
	local := e.expect(e.do("POST", "/accounts", map[string]any{"name": "embed-local", "kind": "openai_compatible", "baseUrl": srv.URL + "/v1"}), 201)
	if local["cloud"] != false {
		t.Fatalf("a localhost endpoint is local: %v", local)
	}
	e.expect(e.do("PUT", "/settings/models", map[string]any{"value": map[string]any{"embedding": map[string]any{"provider": "nope", "model": "m"}}, "version": 1}), 400)
	setEmbedding(t, e, map[string]any{"provider": "embed-local", "model": "concepts", "dimensions": 32})

	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Sem " + uuid.NewString()[:6]}), 201)["id"].(string)
	dir := gitRepo(t, e.repoRoot, "sem-"+uuid.NewString()[:6])
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)["id"].(string)
	e.expect(e.do("POST", "/repositories/"+rid+"/index", nil), 202)
	indexOnce(t, e, []map[string]any{
		{"path": "docs/webhooks.md", "contentHash": "a", "chunks": []map[string]any{{"kind": "doc", "startLine": 1, "endLine": 3,
			"heading": "Webhooks > Delivery", "content": "Failed deliveries are attempted again with exponential backoff."}}},
		{"path": "board.go", "contentHash": "b", "chunks": []map[string]any{{"kind": "code", "startLine": 1, "endLine": 5,
			"heading": "function moveCard", "content": "func moveCard(column string) {}"}}},
	}, nil, "", nil)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if n, err := e.index.EmbedPending(ctx); err != nil {
			t.Fatal(err)
		} else if n == 0 {
			break
		}
	}
	st := e.expect(e.do("GET", "/context/embeddings", nil), 200)
	if st["available"] != true || st["embedded"].(float64) < 2 || st["pending"].(float64) != 0 {
		t.Fatalf("embedded: %v", st)
	}

	// A question sharing no keyword with the doc finds it by meaning.
	hits := e.expect(e.do("GET", "/projects/"+pid+"/context/search?q=what+happens+when+a+call+is+retried", nil), 200)["items"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["path"] != "docs/webhooks.md" || !strings.Contains(hits[0].(map[string]any)["why"].(string), "similar meaning") {
		t.Fatalf("semantic hit first: %v", hits)
	}

	// A cloud account without consent is refused before any call is made.
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "embed-cloud", "kind": "openai_compatible", "baseUrl": "https://embeddings.example.com/v1", "secret": "k-123456789"}), 201)
	before := calls.Load()
	setEmbedding(t, e, map[string]any{"provider": "embed-cloud", "model": "concepts", "dimensions": 32, "allow_cloud": false})
	st = e.expect(e.do("GET", "/context/embeddings", nil), 200)
	if st["available"] != false || st["cloud"] != true || !strings.Contains(st["problem"].(string), "cloud") {
		t.Fatalf("cloud without consent: %v", st)
	}
	if _, err := e.index.EmbedPending(ctx); err == nil {
		t.Fatal("embedding must be refused without consent")
	}
	// Keyword search still works meanwhile.
	if hits := e.expect(e.do("GET", "/projects/"+pid+"/context/search?q=exponential+backoff", nil), 200)["items"].([]any); len(hits) != 1 {
		t.Fatalf("keyword fallback: %v", hits)
	}
	if calls.Load() != before {
		t.Fatal("no request may reach a cloud provider without consent")
	}

	// A model that ignores the configured dimensions parks embedding after one (recorded) call.
	setEmbedding(t, e, map[string]any{"provider": "embed-local", "model": "fixed-32", "dimensions": 64})
	if _, err := e.index.EmbedPending(ctx); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("dimension mismatch: %v", err)
	}
	parked := calls.Load()
	if _, err := e.index.EmbedPending(ctx); err == nil {
		t.Fatal("parked until the configuration changes")
	}
	if calls.Load() != parked {
		t.Fatal("no further paid calls while parked")
	}
	if st := e.expect(e.do("GET", "/context/embeddings", nil), 200); st["available"] != false || !strings.Contains(st["problem"].(string), "dimensions") {
		t.Fatalf("parked status: %v", st)
	}

	// Changing the model makes every chunk pending again (vectors are per model).
	setEmbedding(t, e, map[string]any{"provider": "embed-local", "model": "concepts-v2", "dimensions": 32})
	if st := e.expect(e.do("GET", "/context/embeddings", nil), 200); st["pending"].(float64) < 2 || st["embedded"].(float64) != 0 {
		t.Fatalf("re-embed after model change: %v", st)
	}
}
