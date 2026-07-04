package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"plexus/pkg/llm"
	"plexus/pkg/llm/anthropic"
	"plexus/pkg/llm/openai"
)

// Gateway resolution shared by chat and run: resolving provider/model/key from
// flags + env and building the llm.Provider the brain drives. The interactive,
// live-reconfigurable wrapper (mutableGateway) stays in the chat package.

const (
	defaultOpenAIModel    = "gpt-4o-mini"
	defaultAnthropicModel = "claude-3-5-sonnet-latest"
)

// GatewayConfig is the resolved LLM gateway configuration.
type GatewayConfig struct {
	Provider  string // "openai" | "anthropic"
	Model     string
	BaseURL   string
	APIKey    string
	Debug     bool   // print raw request body + response status (no auth headers)
	Reasoning string // "" | low | medium | high — reasoning effort / thinking budget

	// RawObs, when set, receives the raw LLM request/response (no auth headers) for
	// mirroring to observability. It rides on the config so it survives a rebuild.
	RawObs func([]byte)
}

// ResolveGateway fills a GatewayConfig from explicit flags falling back to env. An
// explicit provider wins; otherwise it is inferred from whichever API key is present
// (defaulting to openai). The key is read from the provider's env var.
func ResolveGateway(provider, model, baseURL, reasoning string, debug bool) GatewayConfig {
	provider = firstNonEmpty(provider, os.Getenv("PLEXUS_LLM_PROVIDER"))
	if provider == "" {
		switch {
		case os.Getenv("OPENAI_API_KEY") != "":
			provider = "openai"
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			provider = "anthropic"
		default:
			provider = "openai"
		}
	}
	return GatewayConfig{
		Provider:  provider,
		Model:     firstNonEmpty(model, os.Getenv("PLEXUS_LLM_MODEL"), defaultModel(provider)),
		BaseURL:   firstNonEmpty(baseURL, os.Getenv("PLEXUS_LLM_BASE_URL")),
		APIKey:    os.Getenv(envKeyName(provider)),
		Reasoning: firstNonEmpty(reasoning, os.Getenv("PLEXUS_REASONING")),
		Debug:     debug,
	}
}

// Build constructs the llm.Provider, or returns a helpful error when no API key is
// available.
func (c GatewayConfig) Build() (llm.Provider, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("no API key for %s — set env %s", c.Provider, envKeyName(c.Provider))
	}
	base := normalizeBaseURL(c.Provider, c.BaseURL)
	switch c.Provider {
	case "openai":
		var opts []openai.Option
		if base != "" {
			opts = append(opts, openai.WithBaseURL(base))
		}
		if c.Debug {
			opts = append(opts, openai.WithMiddleware(debugMiddleware(os.Stdout)))
		}
		if c.RawObs != nil {
			opts = append(opts, openai.WithMiddleware(rawObsMiddleware(c.RawObs)))
		}
		if c.Reasoning != "" {
			opts = append(opts, openai.WithReasoningEffort(c.Reasoning))
		} else if base != "" {
			opts = append(opts, openai.WithSuppressThinking())
		}
		return openai.NewProvider(c.APIKey, c.Model, opts...), nil
	case "anthropic":
		var opts []anthropic.Option
		if base != "" {
			opts = append(opts, anthropic.WithBaseURL(base))
		}
		if c.Debug {
			opts = append(opts, anthropic.WithMiddleware(debugMiddleware(os.Stdout)))
		}
		if c.RawObs != nil {
			opts = append(opts, anthropic.WithMiddleware(rawObsMiddleware(c.RawObs)))
		}
		if c.Reasoning != "" {
			opts = append(opts, anthropic.WithReasoningEffort(c.Reasoning))
		}
		return anthropic.NewProvider(c.APIKey, c.Model, opts...), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want openai or anthropic)", c.Provider)
	}
}

// ErrGatewayUnconfigured is returned by a gateway whose key is not set.
var ErrGatewayUnconfigured = errors.New("no LLM key configured")

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func envKeyName(provider string) string {
	if provider == "anthropic" {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

func defaultModel(provider string) string {
	if provider == "anthropic" {
		return defaultAnthropicModel
	}
	return defaultOpenAIModel
}

// normalizeBaseURL adapts a user base URL to each SDK's convention: the OpenAI SDK
// expects the "/v1" segment in the base, the Anthropic SDK appends it itself.
func normalizeBaseURL(provider, raw string) string {
	if raw == "" {
		return ""
	}
	u := strings.TrimRight(raw, "/")
	switch provider {
	case "openai":
		if !strings.HasSuffix(u, "/v1") {
			u += "/v1"
		}
	case "anthropic":
		u = strings.TrimSuffix(u, "/v1")
	}
	return u
}

// debugMiddleware prints the outgoing request body (pretty JSON; headers omitted
// because they carry the API key) and the response status.
func debugMiddleware(out io.Writer) llm.HTTPMiddleware {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			_ = req.Body.Close()
			if err == nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
				fmt.Fprintf(out, "\033[2m-> %s %s\n%s\033[0m\n", req.Method, req.URL, prettyJSON(body))
			}
		} else {
			fmt.Fprintf(out, "\033[2m-> %s %s\033[0m\n", req.Method, req.URL)
		}
		resp, err := next(req)
		if err != nil {
			fmt.Fprintf(out, "\033[2m<- transport error: %v\033[0m\n", err)
			return resp, err
		}
		if resp != nil {
			fmt.Fprintf(out, "\033[2m<- %s\033[0m\n", resp.Status)
		}
		return resp, err
	}
}

// rawObsMiddleware mirrors the raw LLM request body + response status to a sink (no
// auth headers). Fire-and-forget; the body is restored untouched so streaming works.
func rawObsMiddleware(emit func([]byte)) llm.HTTPMiddleware {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			_ = req.Body.Close()
			if err == nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
				emit([]byte(fmt.Sprintf("-> %s %s\n%s", req.Method, req.URL, prettyJSON(body))))
			}
		} else {
			emit([]byte(fmt.Sprintf("-> %s %s", req.Method, req.URL)))
		}
		resp, err := next(req)
		if err != nil {
			emit([]byte(fmt.Sprintf("<- transport error: %v", err)))
			return resp, err
		}
		if resp != nil {
			emit([]byte("<- " + resp.Status))
		}
		return resp, err
	}
}

func prettyJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return string(b)
	}
	return buf.String()
}
