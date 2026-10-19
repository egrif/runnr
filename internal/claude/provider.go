package claude

import (
	"context"
	"os"
	"strings"
)

// Provider answers questions, streaming the text as it comes.
type Provider interface {
	Name() string      // anthropic | bedrock | openai
	ModelName() string // what's asked
	Stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string)) (Usage, error)
}

// Cache keeps answers locally, by a fingerprint of everything they depend on.
type Cache interface {
	GetAnswer(key string) (string, bool)
	PutAnswer(key, model, answer string) error
}

// FromEnv picks a provider from the environment (nil: none set up):
//
//	RUNNR_AI_PROVIDER  anthropic | bedrock | openai (default: anthropic with
//	                   ANTHROPIC_API_KEY, else openai with OPENAI_API_KEY or
//	                   RUNNR_AI_BASE_URL)
//	RUNNR_AI_MODEL     the model (RUNNR_CLAUDE_MODEL also works)
//	anthropic: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, RUNNR_AI_CACHE_TTL (1h)
//	bedrock:   RUNNR_AI_AWS_PROFILE or AWS_PROFILE, AWS_REGION; the model is
//	           a Bedrock model or inference profile id
//	openai:    RUNNR_AI_BASE_URL (e.g. http://localhost:11434/v1 for
//	           Ollama), RUNNR_AI_API_KEY or OPENAI_API_KEY, or
//	           RUNNR_AI_API_KEY_CMD (a command that prints one);
//	           RUNNR_AI_HEADERS ("Name: value; Name: value");
//	           RUNNR_AI_CACHE_HINTS=1 (mark the runbook for Claude's cache)
//	bifrost:   Greenhouse's AI gateway (OpenAI-compatible, behind SSO):
//	           RUNNR_AI_VIRTUAL_KEY (x-bf-vk), RUNNR_AI_BIFROST_ENV (dev,
//	           staging, prod, prod-eu, prod-apac; default dev), the key from
//	           sso-token-cli -e <env>-bifrost (or RUNNR_AI_API_KEY_CMD), model
//	           as the gateway names it (default DefaultBifrostModel)
func FromEnv() Provider {
	model := firstEnv("RUNNR_AI_MODEL", "RUNNR_CLAUDE_MODEL")
	prov := strings.ToLower(os.Getenv("RUNNR_AI_PROVIDER"))
	if prov == "" {
		switch {
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			prov = "anthropic"
		case os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("RUNNR_AI_BASE_URL") != "":
			prov = "openai"
		default:
			return nil
		}
	}
	switch prov {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil
		}
		return &Client{Key: key, Model: or(model, DefaultModel), BaseURL: os.Getenv("ANTHROPIC_BASE_URL"), CacheTTL: os.Getenv("RUNNR_AI_CACHE_TTL")}
	case "bedrock":
		return &Bedrock{
			Profile: firstEnv("RUNNR_AI_AWS_PROFILE", "AWS_PROFILE"),
			Region:  or(firstEnv("AWS_REGION", "AWS_DEFAULT_REGION"), "us-east-1"),
			Model:   or(model, DefaultBedrockModel),
		}
	case "openai":
		return &OpenAI{
			BaseURL:    or(os.Getenv("RUNNR_AI_BASE_URL"), "https://api.openai.com/v1"),
			Key:        firstEnv("RUNNR_AI_API_KEY", "OPENAI_API_KEY"),
			KeyCmd:     os.Getenv("RUNNR_AI_API_KEY_CMD"),
			Headers:    parseHeaders(os.Getenv("RUNNR_AI_HEADERS")),
			CacheHints: os.Getenv("RUNNR_AI_CACHE_HINTS") != "",
			Model:      or(model, DefaultOpenAIModel),
		}
	case "bifrost":
		env := or(os.Getenv("RUNNR_AI_BIFROST_ENV"), "dev")
		host, ok := bifrostHosts[env]
		if !ok {
			host = env // a host name
		}
		h := parseHeaders(os.Getenv("RUNNR_AI_HEADERS"))
		if vk := os.Getenv("RUNNR_AI_VIRTUAL_KEY"); vk != "" {
			h["x-bf-vk"] = vk
		}
		return &OpenAI{
			Label:   "bifrost",
			BaseURL: or(os.Getenv("RUNNR_AI_BASE_URL"), "https://"+host+"/v1"),
			Key:     os.Getenv("RUNNR_AI_API_KEY"),
			KeyCmd:  or(os.Getenv("RUNNR_AI_API_KEY_CMD"), "sso-token-cli -e "+env+"-bifrost"),
			Headers: h,
			// Claude's cache markers, for Claude models only.
			CacheHints: os.Getenv("RUNNR_AI_CACHE_HINTS") != "0" && strings.Contains(strings.ToLower(model), "claude"),
			Model:      or(model, DefaultBifrostModel),
		}
	}
	return nil
}

// DefaultBifrostModel is the model Greenhouse's agents use through the
// gateway; it serves the models (and providers) it's configured for.
const DefaultBifrostModel = "gpt-5.4"

// bifrostHosts are the gateway's environments.
var bifrostHosts = map[string]string{
	"dev":       "dev-bifrost.dev-use1-0.gh.team",
	"staging":   "staging-bifrost.prod-use1-0.gh.team",
	"prod":      "bifrost.prod-use1-0.gh.team",
	"prod-eu":   "bifrost.prod-euc1-0.gh.team",
	"prod-apac": "bifrost.prod-apse2-0.gh.team",
}

// parseHeaders reads "Name: value; Name: value".
func parseHeaders(s string) map[string]string {
	h := map[string]string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' }) {
		if k, v, ok := strings.Cut(part, ":"); ok && strings.TrimSpace(k) != "" {
			h[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return h
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
