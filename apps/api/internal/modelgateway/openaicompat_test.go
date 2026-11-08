package modelgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAICompat(t *testing.T) {
	var got map[string]any
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"text-embedding-nomic"},{"id":"qwen2.5-coder-7b"}]}`))
		case "/v1/chat/completions":
			if fail {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"model loading"}`))
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"model":"qwen2.5-coder-7b","choices":[{"message":{"content":"{\"action\":\"final\",\"final\":{}}"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := NewOpenAICompat("lmstudio", ProviderConfig{BaseURL: srv.URL + "/v1", Timeout: 5 * time.Second, InputPerMTok: 1, OutputPerMTok: 2})
	temp := 0.0
	r, err := p.Complete(context.Background(), ProviderRequest{
		System: "sys", Messages: []Message{{Role: "user", Content: "q"}, {Role: "tool", Name: "read_file", Content: "data"}},
		Schema: json.RawMessage(`{"type":"object"}`), Temperature: &temp, MaxOutputTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "qwen2.5-coder-7b" {
		t.Errorf("should auto-select the loaded chat model (skipping embeddings), got %v", got["model"])
	}
	if rf, _ := got["response_format"].(map[string]any); rf["type"] != "json_schema" {
		t.Errorf("response_format not sent: %v", got["response_format"])
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 3 || msgs[2].(map[string]any)["role"] != "user" {
		t.Errorf("tool result should be sent as a user message: %v", msgs)
	}
	if r.Usage.InputTokens != 11 || r.CostUSD != (11*1+7*2)/1e6 {
		t.Errorf("usage/cost: %+v", r)
	}

	fail = true
	if _, err := p.Complete(context.Background(), ProviderRequest{Messages: []Message{{Role: "user", Content: "q"}}}); err == nil || !transient(err) {
		t.Fatalf("503 should be transient: %v", err)
	}
	down := NewOpenAICompat("lmstudio", ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Timeout: time.Second})
	if err := down.Health(context.Background()); err == nil {
		t.Fatal("health should fail when the server is down")
	}
}

// Google AI Studio's OpenAI-compatible API lists "models/<id>" but takes "<id>".
func TestOpenAICompatStripsModelsPrefix(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/openai/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"models/text-embedding-004"},{"id":"models/gemini-2.5-flash"}]}`))
		case "/v1beta/openai/chat/completions":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := NewOpenAICompat("gemini", ProviderConfig{BaseURL: srv.URL + "/v1beta/openai", Timeout: 5 * time.Second})
	r, err := p.Complete(context.Background(), ProviderRequest{System: "s", Messages: []Message{{Role: "user", Content: "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gemini-2.5-flash" || r.Model != "gemini-2.5-flash" {
		t.Fatalf("model sent %v, reported %v", got["model"], r.Model)
	}
}
