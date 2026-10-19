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
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// The example from AWS's Signature Version 4 documentation (IAM ListUsers).
func TestSignV4MatchesAWSExample(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	now, _ := time.Parse("20060102T150405Z", "20150830T123600Z")
	signV4(req, nil, AWSCreds{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, "us-east-1", "iam", now)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBedrockConverse(t *testing.T) {
	var got map[string]any
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.EscapedPath(), r.Header.Get("authorization")
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"From Bedrock."}]}},"usage":{"inputTokens":9,"outputTokens":3,"cacheReadInputTokens":4000}}`)
	}))
	defer srv.Close()
	calls := 0
	b := &Bedrock{Region: "us-east-1", Model: "us.anthropic.claude-x-v1:0", Endpoint: srv.URL,
		Creds: func(context.Context) (AWSCreds, error) {
			calls++
			return AWSCreds{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TOK", Expiration: time.Now().Add(time.Hour)}, nil
		}}
	var text strings.Builder
	for range 2 {
		text.Reset()
		u, err := b.Stream(context.Background(), []Block{{Text: "instr"}, {Text: "runbook", Cache: true}}, nil, "q", 100, func(s string) { text.WriteString(s) })
		if err != nil || text.String() != "From Bedrock." || u.CacheRead != 4000 {
			t.Fatalf("%v %q %+v", err, text.String(), u)
		}
	}
	if calls != 1 {
		t.Fatalf("credentials fetched %d times, want 1 (reused)", calls)
	}
	if path != "/model/us.anthropic.claude-x-v1%3A0/converse" || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AK/") || !strings.Contains(auth, "x-amz-security-token") {
		t.Fatalf("path %s auth %s", path, auth)
	}
	sys := got["system"].([]any)
	if len(sys) != 3 || sys[2].(map[string]any)["cachePoint"] == nil {
		t.Fatalf("a cache point should follow the runbook: %v", sys)
	}
	if _, err := (&Bedrock{}).Stream(context.Background(), nil, nil, "q", 1, func(string) {}); err == nil || !strings.Contains(err.Error(), "RUNNR_AI_MODEL") {
		t.Fatalf("no model: %v", err)
	}
}

func TestOpenAICompatible(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		for _, c := range []string{`{"choices":[{"delta":{"content":"Local "}}]}`, `{"choices":[{"delta":{"content":"model."}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":5000,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":4800}}}`, `[DONE]`} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
	}))
	defer srv.Close()
	o := &OpenAI{BaseURL: srv.URL + "/v1", Model: "llama3"}
	var text strings.Builder
	u, err := o.Stream(context.Background(), []Block{{Text: "a"}, {Text: "b", Cache: true}}, []Message{{Role: "user", Content: "q0"}, {Role: "assistant", Content: "a0"}}, "q1", 100, func(s string) { text.WriteString(s) })
	if err != nil || text.String() != "Local model." || u.CacheRead != 4800 || u.Input != 200 || u.Output != 4 {
		t.Fatalf("%v %q %+v", err, text.String(), u)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["content"] != "q1" {
		t.Fatalf("messages %v", msgs)
	}
}

type memCache map[string]string

func (m memCache) GetAnswer(k string) (string, bool) { a, ok := m[k]; return a, ok }
func (m memCache) PutAnswer(k, _, a string) error    { m[k] = a; return nil }

type countingProvider struct{ calls int }

func (c *countingProvider) Name() string      { return "fake" }
func (c *countingProvider) ModelName() string { return "m" }
func (c *countingProvider) Stream(_ context.Context, _ []Block, _ []Message, q string, _ int, on func(string)) (Usage, error) {
	c.calls++
	on("answer to " + q)
	return Usage{Input: 10}, nil
}

func TestLocalAnswerCache(t *testing.T) {
	rb, _ := runbook.Parse([]byte("name: t\nsteps: [{name: a, run: 'echo a'}, {name: b, run: 'echo b'}]\n"))
	p, cache := &countingProvider{}, memCache{}
	ask := func(rb *runbook.Runbook, step int, fresh bool) (string, Usage) {
		var s strings.Builder
		u, err := Explain(context.Background(), p, cache, rb, nil, nil, step, "", nil, fresh, func(t string) { s.WriteString(t) })
		if err != nil {
			t.Fatal(err)
		}
		return s.String(), u
	}
	a1, _ := ask(rb, 1, false)
	a2, u2 := ask(rb, 1, false) // same question, same runbook: from the local cache
	if a1 != a2 || !u2.Local || p.calls != 1 {
		t.Fatalf("%q %q %+v calls=%d", a1, a2, u2, p.calls)
	}
	ask(rb, 2, false) // another step asks
	ask(rb, 1, true)  // fresh asks again
	changed, _ := runbook.Parse([]byte("name: t\nsteps: [{name: a, run: 'echo A'}, {name: b, run: 'echo b'}]\n"))
	ask(changed, 1, false) // the runbook changed: asks
	if p.calls != 4 {
		t.Fatalf("calls %d, want 4", p.calls)
	}
}

func TestGatewayKeyCommandHeadersAndCacheHints(t *testing.T) {
	var got map[string]any
	var auths, vks []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("authorization"))
		vks = append(vks, r.Header.Get("x-bf-vk"))
		if len(auths) == 1 {
			w.WriteHeader(401) // the first token has expired
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	dir := t.TempDir()
	// The key command prints a new token each time it runs.
	cmd := fmt.Sprintf(`n=$(cat %[1]s/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]s/n; echo "tok$n"`, dir)
	o := &OpenAI{Label: "bifrost", BaseURL: srv.URL + "/v1", KeyCmd: cmd, Headers: parseHeaders("x-bf-vk: sk-bf-123; X-Guardrails: enabled"), CacheHints: true, Model: "anthropic/claude-x"}
	var text strings.Builder
	if _, err := o.Stream(context.Background(), []Block{{Text: "instr"}, {Text: "runbook", Cache: true}}, nil, "q", 10, func(s string) { text.WriteString(s) }); err != nil || text.String() != "ok" {
		t.Fatalf("%v %q", err, text.String())
	}
	if strings.Join(auths, ",") != "Bearer tok1,Bearer tok2" || vks[1] != "sk-bf-123" {
		t.Fatalf("auths %v vks %v", auths, vks)
	}
	parts := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["cache_control"] == nil || parts[0].(map[string]any)["cache_control"] != nil {
		t.Fatalf("system parts %v", parts)
	}
	if o.Name() != "bifrost" || got["model"] != "anthropic/claude-x" {
		t.Fatalf("%s %v", o.Name(), got["model"])
	}
}

// Claude through Bifrost goes to its Anthropic Messages API, as
// Greenhouse's agents do: the SSO token as bearer (renewed after a 401),
// the virtual key, and the runbook marked for caching.
func TestBifrostClaudeViaMessagesAPI(t *testing.T) {
	var paths, auths, vks, apiKeys []string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		auths = append(auths, r.Header.Get("authorization"))
		vks = append(vks, r.Header.Get("x-bf-vk"))
		apiKeys = append(apiKeys, r.Header.Get("x-api-key"))
		if len(auths) == 1 {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"token expired"}}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
	}))
	defer srv.Close()
	dir := t.TempDir()
	t.Setenv("RUNNR_AI_PROVIDER", "bifrost")
	t.Setenv("RUNNR_AI_BASE_URL", srv.URL+"/v1")
	t.Setenv("RUNNR_AI_VIRTUAL_KEY", "sk-bf-1")
	t.Setenv("RUNNR_AI_MODEL", "anthropic/claude-x")
	t.Setenv("RUNNR_AI_API_KEY_CMD", fmt.Sprintf(`n=$(cat %[1]s/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]s/n; echo "tok$n"`, dir))
	p := FromEnv()
	c, ok := p.(*Client)
	if !ok || p.Name() != "bifrost" || c.Model != "claude-x" {
		t.Fatalf("provider %T %s %s", p, p.Name(), p.ModelName())
	}
	var text strings.Builder
	if _, err := p.Stream(context.Background(), []Block{{Text: "instr"}, {Text: "runbook", Cache: true}}, nil, "q", 10, func(s string) { text.WriteString(s) }); err != nil || text.String() != "ok" {
		t.Fatalf("%v %q", err, text.String())
	}
	if paths[1] != "/v1/messages" || strings.Join(auths, ",") != "Bearer tok1,Bearer tok2" || vks[1] != "sk-bf-1" || apiKeys[1] == "" {
		t.Fatalf("paths %v auths %v vks %v keys %v", paths, auths, vks, apiKeys)
	}
	if sys := got["system"].([]any); sys[1].(map[string]any)["cache_control"] == nil || got["model"] != "claude-x" {
		t.Fatalf("request %v", got)
	}
	// Other providers' models still go through the OpenAI-compatible API.
	t.Setenv("RUNNR_AI_MODEL", "openai/gpt-x")
	if o, ok := FromEnv().(*OpenAI); !ok || o.Model != "openai/gpt-x" || o.BaseURL != srv.URL+"/v1" {
		t.Fatalf("%T", FromEnv())
	}
}
