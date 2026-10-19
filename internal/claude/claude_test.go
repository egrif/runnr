package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

func TestExplainCachesTheRunbook(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":3400,"cache_creation_input_tokens":0}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It derives "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the region."}}`,
			`{"type":"message_delta","delta":{},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", ev)
		}
	}))
	defer srv.Close()

	rb, err := runbook.Parse([]byte(`name: t
sensitive: {api: 'token={{SENSITIVE}}'}
steps:
  - {name: setup, always: true, run: 'region=use1; curl "x?token=abc123"', outputs: [region]}
  - {name: deploy, run: 'kpsql "$region"', confirm: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	lib := &runbook.Library{Name: "h", Functions: map[string]runbook.Function{"kpsql": {Body: "psql \"$1\""}}}
	c := &Client{Key: "k", Model: "m", BaseURL: srv.URL}
	var text strings.Builder
	u, err := Explain(context.Background(), c, nil, rb, nil, []*runbook.Library{lib}, 2, "", nil, false, func(s string) { text.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "It derives the region." || u.CacheRead != 3400 || u.Output != 7 {
		t.Fatalf("%q %+v", text.String(), u)
	}
	if hdr.Get("x-api-key") != "k" || hdr.Get("anthropic-version") == "" || got["stream"] != true || got["model"] != "m" {
		t.Fatalf("request %v %v", hdr, got)
	}
	sys := got["system"].([]any)
	ctxBlock := sys[1].(map[string]any)
	if _, cached := ctxBlock["cache_control"]; !cached || sys[0].(map[string]any)["cache_control"] != nil {
		t.Fatalf("only the runbook context (the end of the prefix) is marked for caching: %v", sys)
	}
	ctxText := ctxBlock["text"].(string)
	for _, want := range []string{"1. setup [run; always (setup); publishes region]", "2. deploy [run; confirm]", "kpsql() {", "token=****"} {
		if !strings.Contains(ctxText, want) {
			t.Errorf("context lacks %q:\n%s", want, ctxText)
		}
	}
	if strings.Contains(ctxText, "abc123") {
		t.Fatal("a credential reached the context")
	}
	msgs := got["messages"].([]any)
	if q := msgs[0].(map[string]any)["content"]; q != `Explain step 2 ("deploy").` {
		t.Fatalf("question %q", q)
	}
	// A follow-up goes after the conversation so far, as asked.
	hist := []Message{{Role: "user", Content: `Explain step 2 ("deploy").`}, {Role: "assistant", Content: "It derives the region."}}
	Explain(context.Background(), c, nil, rb, nil, nil, 2, "And if it's empty?", hist, false, func(string) {})
	if msgs := got["messages"].([]any); len(msgs) != 3 || msgs[2].(map[string]any)["content"] != "And if it's empty?" {
		t.Fatalf("follow-up messages %v", msgs)
	}
	if _, err := Explain(context.Background(), c, nil, rb, nil, nil, 9, "", nil, false, func(string) {}); err == nil {
		t.Fatal("no step 9")
	}
}

func TestAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()
	c := &Client{Key: "bad", Model: "m", BaseURL: srv.URL}
	if _, err := c.Stream(context.Background(), nil, nil, "hi", 10, func(string) {}); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("%v", err)
	}
}
