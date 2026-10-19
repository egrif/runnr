package claude

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBedrockModel is empty: Bedrock model ids depend on the account
// and region (see aws bedrock list-inference-profiles), so set RUNNR_AI_MODEL.
const DefaultBedrockModel = ""

// Bedrock calls Claude (or another model) through Amazon Bedrock's Converse
// API, signed with the AWS profile's credentials (SSO included, from the
// AWS CLI). Prompt caching works there too, through a cache point after
// the runbook. The answer arrives whole rather than streamed.
type Bedrock struct {
	Profile string
	Region  string
	Model   string
	HTTP    *http.Client
	// Endpoint overrides https://bedrock-runtime.<region>.amazonaws.com.
	Endpoint string
	// Creds gets credentials (default: the AWS CLI's export-credentials).
	Creds func(ctx context.Context) (AWSCreds, error)

	mu    sync.Mutex
	creds AWSCreds
}

// AWSCreds are temporary AWS credentials.
type AWSCreds struct {
	AccessKeyID     string    `json:"AccessKeyId"`
	SecretAccessKey string    `json:"SecretAccessKey"`
	SessionToken    string    `json:"SessionToken"`
	Expiration      time.Time `json:"Expiration"`
}

func (b *Bedrock) Name() string      { return "bedrock" }
func (b *Bedrock) ModelName() string { return b.Model }

func (b *Bedrock) Stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string)) (Usage, error) {
	var usage Usage
	if b.Model == "" {
		return usage, errors.New("set RUNNR_AI_MODEL to a Bedrock model or inference profile id (aws bedrock list-inference-profiles)")
	}
	type content struct {
		Text       string `json:"text,omitempty"`
		CachePoint any    `json:"cachePoint,omitempty"`
	}
	var sys []content
	for _, bl := range system {
		sys = append(sys, content{Text: bl.Text})
		if bl.Cache {
			sys = append(sys, content{CachePoint: map[string]string{"type": "default"}})
		}
	}
	type message struct {
		Role    string    `json:"role"`
		Content []content `json:"content"`
	}
	var msgs []message
	for _, m := range history {
		msgs = append(msgs, message{Role: m.Role, Content: []content{{Text: m.Content}}})
	}
	msgs = append(msgs, message{Role: "user", Content: []content{{Text: question}}})
	body, err := json.Marshal(map[string]any{"system": sys, "messages": msgs, "inferenceConfig": map[string]int{"maxTokens": maxTokens}})
	if err != nil {
		return usage, err
	}
	creds, err := b.credentials(ctx)
	if err != nil {
		return usage, err
	}
	endpoint := b.Endpoint
	if endpoint == "" {
		endpoint = "https://bedrock-runtime." + b.Region + ".amazonaws.com"
	}
	path := "/model/" + awsEscape(b.Model) + "/converse"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+path, bytes.NewReader(body))
	if err != nil {
		return usage, err
	}
	req.URL.RawPath = path
	req.Header.Set("content-type", "application/json")
	signV4(req, body, creds, b.Region, "bedrock", time.Now())
	hc := b.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return usage, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		var e struct{ Message string }
		if json.Unmarshal(rb, &e) == nil && e.Message != "" {
			return usage, fmt.Errorf("bedrock: %s (%d)", e.Message, res.StatusCode)
		}
		return usage, fmt.Errorf("bedrock: %s", res.Status)
	}
	var out struct {
		Output struct {
			Message struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"message"`
		} `json:"output"`
		Usage struct {
			Input      int `json:"inputTokens"`
			Output     int `json:"outputTokens"`
			CacheRead  int `json:"cacheReadInputTokens"`
			CacheWrite int `json:"cacheWriteInputTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return usage, fmt.Errorf("bedrock: %w", err)
	}
	for _, c := range out.Output.Message.Content {
		if c.Text != "" {
			onText(c.Text)
		}
	}
	u := out.Usage
	return Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}, nil
}

// credentials are the profile's, from the AWS CLI (which handles SSO),
// reused until a few minutes before they expire.
func (b *Bedrock) credentials(ctx context.Context) (AWSCreds, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.creds.AccessKeyID != "" && (b.creds.Expiration.IsZero() || time.Until(b.creds.Expiration) > 5*time.Minute) {
		return b.creds, nil
	}
	get := b.Creds
	if get == nil {
		get = func(ctx context.Context) (AWSCreds, error) {
			args := []string{"configure", "export-credentials", "--format", "process"}
			if b.Profile != "" {
				args = append(args, "--profile", b.Profile)
			}
			out, err := exec.CommandContext(ctx, "aws", args...).Output()
			if err != nil {
				msg := err.Error()
				var ee *exec.ExitError
				if errors.As(err, &ee) && len(ee.Stderr) > 0 {
					msg = strings.TrimSpace(string(ee.Stderr))
				}
				return AWSCreds{}, fmt.Errorf("aws credentials (profile %q): %s (try aws sso login)", b.Profile, msg)
			}
			var c AWSCreds
			return c, json.Unmarshal(out, &c)
		}
	}
	c, err := get(ctx)
	if err == nil {
		b.creds = c
	}
	return c, err
}

// signV4 signs a request with AWS Signature Version 4.
func signV4(req *http.Request, body []byte, c AWSCreds, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("x-amz-security-token", c.SessionToken)
	}
	host := req.URL.Host
	req.Host = host
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		headers[strings.ToLower(k)] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	// Paths are encoded once in the request and again here (all services but S3).
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	var segs []string
	for _, s := range strings.Split(path, "/") {
		segs = append(segs, awsEscape(s))
	}
	canonURI := strings.Join(segs, "/")
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			qs = append(qs, awsEscape(k)+"="+awsEscape(v))
		}
	}
	payload := sha256.Sum256(body)
	canonical := strings.Join([]string{req.Method, canonURI, strings.Join(qs, "&"), canonHeaders.String(), signed, hex.EncodeToString(payload[:])}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	ch := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch[:])
	key := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKeyID, scope, signed, sig))
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// awsEscape percent-encodes everything but unreserved characters, as AWS
// expects (url.PathEscape leaves ':' and others alone).
func awsEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
