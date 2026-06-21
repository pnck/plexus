//go:build !nochat

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"plexus/pkg/llm"
	"plexus/pkg/llm/anthropic"
	"plexus/pkg/llm/openai"
)

var (
	chatProvider string
	chatModel    string
	chatSystem   string
	chatBaseURL  string
)

const (
	defaultOpenAIModel    = "gpt-4o-mini"
	defaultAnthropicModel = "claude-3-5-sonnet-latest"
)

// slashCommands lists every recognized slash command (including aliases). It
// drives both the /help output and the Tab-completion set.
var slashCommands = []string{
	"/provider", "/model", "/models", "/key", "/system", "/debug",
	"/status", "/show", "/reset", "/clear",
	"/help", "/?", "/exit", "/quit", "/bye",
}

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Interactive chat mode to verify LLM connectivity",
	RunE: func(cmd *cobra.Command, args []string) error {
		s := newSession()
		return runREPL(s)
	},
}

// session holds the mutable chat configuration, adjustable via slash commands.
type session struct {
	provider string
	model    string
	system   string
	baseURL  string
	apiKey   string
	debug    bool
}

// newSession resolves the initial config. An explicit provider (flag/env) wins;
// otherwise the provider is inferred from whichever API key is present.
func newSession() *session {
	provider := firstNonEmpty(chatProvider, os.Getenv("PLEXUS_LLM_PROVIDER"))
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
	s := &session{
		provider: provider,
		system:   firstNonEmpty(chatSystem, os.Getenv("PLEXUS_SYSTEM_PROMPT")),
		baseURL:  firstNonEmpty(chatBaseURL, os.Getenv("PLEXUS_LLM_BASE_URL")),
	}
	s.model = firstNonEmpty(chatModel, os.Getenv("PLEXUS_LLM_MODEL"), defaultModel(provider))
	s.apiKey = os.Getenv(envKeyName(provider))
	return s
}

// build constructs the provider from the current session config. It returns a
// helpful error (rather than exiting) when no API key is available, so the REPL
// can keep running and let the user set one interactively.
func (s *session) build() (llm.Provider, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("no API key for %s — set it with `/key <value>` or env %s",
			s.provider, envKeyName(s.provider))
	}
	base := normalizeBaseURL(s.provider, s.baseURL)
	switch s.provider {
	case "openai":
		var opts []openai.Option
		if base != "" {
			opts = append(opts, openai.WithBaseURL(base))
		}
		if s.debug {
			opts = append(opts, openai.WithMiddleware(debugMiddleware(os.Stdout)))
		}
		return openai.NewProvider(s.apiKey, s.model, opts...), nil
	case "anthropic":
		var opts []anthropic.Option
		if base != "" {
			opts = append(opts, anthropic.WithBaseURL(base))
		}
		if s.debug {
			opts = append(opts, anthropic.WithMiddleware(debugMiddleware(os.Stdout)))
		}
		return anthropic.NewProvider(s.apiKey, s.model, opts...), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want openai or anthropic)", s.provider)
	}
}

// completer builds the Tab-completion set from the recognized slash commands.
func completer() *readline.PrefixCompleter {
	items := make([]readline.PrefixCompleterInterface, 0, len(slashCommands))
	for _, c := range slashCommands {
		items = append(items, readline.PcItem(c))
	}
	return readline.NewPrefixCompleter(items...)
}

func runREPL(s *session) error {
	out := os.Stdout
	fmt.Fprintln(out, "Plexus chat — interactive LLM gateway tester")
	printStatus(out, s)
	printHelp(out)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "> ",
		AutoComplete:    completer(),
		InterruptPrompt: "^C",
		EOFPrompt:       "",
		// Keep ^C/^D from leaking to the terminal as raw glyphs; we handle them.
		HistoryLimit: 1000,
	})
	if err != nil {
		return err
	}
	defer func() { _ = rl.Close() }()

	history := append([]llm.Message(nil), systemBase(s.system)...)

	for {
		line, err := rl.Readline()
		if err != nil {
			switch {
			case errors.Is(err, readline.ErrInterrupt):
				// Ctrl-C while editing: clear the line and continue. Do not dump ^C.
				fmt.Fprintln(out, "(use /exit or /bye to quit)")
				continue
			case errors.Is(err, io.EOF):
				// Ctrl-D / EOF: quit cleanly.
				return nil
			default:
				return err
			}
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			quit, rebuild := handleCommand(out, s, input)
			if quit {
				return nil
			}
			if rebuild {
				history = append([]llm.Message(nil), systemBase(s.system)...)
			}
			continue
		}

		p, err := s.build()
		if err != nil {
			fmt.Fprintf(out, "[%v]\n", err)
			continue
		}

		history = append(history, llm.Message{Role: llm.RoleUser, Content: input})
		assistant, ok := streamTurn(p, out, history)
		if !ok {
			history = history[:len(history)-1] // drop the turn that produced no response
			continue
		}
		history = append(history, llm.Message{Role: llm.RoleAssistant, Content: assistant})
	}
}

// handleCommand processes a slash command, mutating the session. quit=true exits
// the REPL; rebuild=true asks the caller to reset the conversation history.
func handleCommand(out *os.File, s *session, input string) (quit, rebuild bool) {
	name, arg, _ := strings.Cut(input, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "/exit", "/quit", "/bye":
		return true, false
	case "/reset", "/clear":
		fmt.Fprintln(out, "[history cleared]")
		return false, true
	case "/status", "/show":
		printStatus(out, s)
	case "/help", "/?":
		printHelp(out)
	case "/provider":
		if arg != "openai" && arg != "anthropic" {
			fmt.Fprintln(out, "[usage: /provider openai|anthropic]")
			return false, false
		}
		s.provider = arg // only switch the wire protocol; keep model/key/base-url/system as-is
		printStatus(out, s)
	case "/model":
		if arg == "" {
			fmt.Fprintln(out, "[usage: /model <id>]")
			return false, false
		}
		s.model = arg
		printStatus(out, s)
	case "/models":
		listModels(out, s)
	case "/debug":
		switch arg {
		case "on":
			s.debug = true
			fmt.Fprintln(out, "[debug on — raw request body + response status shown]")
		case "off":
			s.debug = false
			fmt.Fprintln(out, "[debug off]")
		default:
			state := "off"
			if s.debug {
				state = "on"
			}
			fmt.Fprintf(out, "[usage: /debug on|off  (currently %s)]\n", state)
		}
	case "/key":
		if arg == "" {
			fmt.Fprintln(out, "[usage: /key <api-key>]")
			return false, false
		}
		s.apiKey = arg
		fmt.Fprintln(out, "[api key set]")
	case "/system":
		s.system = arg
		fmt.Fprintln(out, "[system prompt set; history reset]")
		return false, true
	default:
		fmt.Fprintf(out, "[unknown command %q — try /help]\n", name)
	}
	return false, false
}

// listModels enumerates the configured provider's available models, if the
// provider supports it. It reuses the same "no API key" hint as a normal turn.
func listModels(out *os.File, s *session) {
	p, err := s.build()
	if err != nil {
		fmt.Fprintf(out, "[%v]\n", err)
		return
	}
	lister, ok := p.(llm.ModelLister)
	if !ok {
		fmt.Fprintf(out, "[provider %q does not support listing models]\n", s.provider)
		return
	}
	ids, err := lister.ListModels(context.Background())
	if err != nil {
		fmt.Fprintf(out, "[error: %v]\n", err)
		return
	}
	for _, id := range ids {
		fmt.Fprintln(out, id)
	}
	fmt.Fprintf(out, "\033[2m[%d models]\033[0m\n", len(ids))
}

// streamTurn runs one generation, printing text as it arrives, and returns the
// accumulated assistant text. ok is false when an error occurred. Ctrl-C during
// the response cancels just this request and returns to the prompt.
func streamTurn(p llm.Provider, out *os.File, msgs []llm.Message) (string, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// While streaming, readline is not in its read loop, so its key listener
	// never fires. Instead catch the OS interrupt directly and cancel just this
	// request's context — returning to the prompt rather than exiting.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	stream, err := p.GenerateStream(ctx, msgs, nil)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(out, "\n[cancelled]")
			return "", false
		}
		fmt.Fprintf(out, "\n[error: %v]\n", err)
		return "", false
	}
	defer func() { _ = stream.Close() }()

	var (
		assistant strings.Builder
		usage     *llm.Usage
	)

	for stream.Next() {
		ev := stream.Current()
		if ev.Error != nil {
			fmt.Fprintf(out, "\n[error: %v]\n", ev.Error)
			return "", false
		}
		if ev.DeltaText != "" {
			fmt.Fprint(out, ev.DeltaText)
			_ = out.Sync()
			assistant.WriteString(ev.DeltaText)
		}
		if ev.Usage != nil {
			usage = ev.Usage
		}
	}

	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			// Cancelled by Ctrl-C: report cleanly and keep the partial text.
			fmt.Fprintln(out, "\n[cancelled]")
			return assistant.String(), assistant.Len() > 0
		}
		fmt.Fprintf(out, "\n[error: %v]\n", err)
		return "", false
	}

	fmt.Fprintln(out)
	if usage != nil {
		fmt.Fprintf(out, "\033[2m[tokens: in=%d out=%d total=%d]\033[0m\n",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}

	return assistant.String(), true
}

func printStatus(out *os.File, s *session) {
	keyStatus := "MISSING"
	if s.apiKey != "" {
		keyStatus = "set"
	}
	fmt.Fprintf(out, "[provider=%s model=%s key=%s", s.provider, s.model, keyStatus)
	if s.baseURL != "" {
		fmt.Fprintf(out, " base-url=%s", normalizeBaseURL(s.provider, s.baseURL))
	}
	if s.system != "" {
		fmt.Fprint(out, " system=set")
	}
	fmt.Fprintln(out, "]")
}

func printHelp(out *os.File) {
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  /provider <p>   switch provider (openai|anthropic)")
	fmt.Fprintln(out, "  /model <id>     set the model id")
	fmt.Fprintln(out, "  /models         list the provider's available models")
	fmt.Fprintln(out, "  /key <v>        set the API key for this session")
	fmt.Fprintln(out, "  /system <txt>   set the system prompt (resets history)")
	fmt.Fprintln(out, "  /debug on|off   show raw request body + response status")
	fmt.Fprintln(out, "  /status, /show  show current config")
	fmt.Fprintln(out, "  /reset, /clear  clear the conversation history")
	fmt.Fprintln(out, "  /help, /?       show this help")
	fmt.Fprintln(out, "  /exit, /bye     quit (also Ctrl-D)")
}

// debugMiddleware prints the outgoing request body (pretty-printed JSON; request
// headers are omitted because they carry the API key) and the response status +
// a few non-secret headers. The response body is left untouched so streaming
// still works — its content shows as the normal streamed text.
func debugMiddleware(out io.Writer) llm.HTTPMiddleware {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			_ = req.Body.Close()
			if err == nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
				fmt.Fprintf(out, "\033[2m→ %s %s\n%s\033[0m\n", req.Method, req.URL, prettyJSON(body))
			}
		} else {
			fmt.Fprintf(out, "\033[2m→ %s %s\033[0m\n", req.Method, req.URL)
		}

		resp, err := next(req)
		if err != nil {
			fmt.Fprintf(out, "\033[2m← transport error: %v\033[0m\n", err)
			return resp, err
		}
		if resp != nil {
			fmt.Fprintf(out, "\033[2m← %s\033[0m\n", resp.Status)
			for _, h := range []string{"Content-Type", "X-Request-Id", "Request-Id"} {
				if v := resp.Header.Get(h); v != "" {
					fmt.Fprintf(out, "\033[2m  %s: %s\033[0m\n", h, v)
				}
			}
		}
		return resp, err
	}
}

// prettyJSON indents a JSON document; non-JSON input is returned unchanged.
func prettyJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return string(b)
	}
	return buf.String()
}

func systemBase(system string) []llm.Message {
	if system == "" {
		return nil
	}
	return []llm.Message{{Role: llm.RoleSystem, Content: system}}
}

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

// normalizeBaseURL adapts a user-supplied base URL to each SDK's convention so
// the same endpoint works for either provider: the OpenAI SDK expects the API
// version segment in the base (".../v1"), while the Anthropic SDK appends it
// itself and must NOT have it. So the user can pass "https://host" or
// "https://host/v1" and we add or strip "/v1" to match the current provider.
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

func init() {
	rootCmd.AddCommand(chatCmd)

	chatCmd.Flags().StringVar(&chatProvider, "provider", "", "LLM provider: openai | anthropic (env PLEXUS_LLM_PROVIDER; auto-detected from available key if unset)")
	chatCmd.Flags().StringVar(&chatModel, "model", "", "Model id (env PLEXUS_LLM_MODEL, provider-specific default)")
	chatCmd.Flags().StringVar(&chatSystem, "system", "", "Optional system prompt (env PLEXUS_SYSTEM_PROMPT)")
	chatCmd.Flags().StringVar(&chatBaseURL, "base-url", "", "Optional API base URL (env PLEXUS_LLM_BASE_URL)")
}
