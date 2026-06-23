package openai

import (
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/packages/respjson"
	"plexus/pkg/llm"
)

// Thinking on the OpenAI-compatible side is not standardized. We handle two
// shapes here:
//
//   - reasoning_content: a non-standard delta field (DeepSeek-R1 and friends).
//     The official SDK drops it into Delta.JSON.ExtraFields as raw JSON; we pull
//     it out and emit it as thinking.
//   - <think>…</think> inline in the content stream (R1 distills, QwQ, …): a
//     streaming splitter routes text inside the tags to thinking and the rest to
//     the answer, tolerating tags split across chunks.

// reasoningExtra pulls a streamed thinking delta from whichever non-standard
// field the endpoint uses. There is no standard: DeepSeek (and its direct
// imitators) use "reasoning_content"; OpenRouter and many proxies (burn.hair,
// etc.) relay it as "reasoning". We try both so reasoning shows regardless of
// which OpenAI-compatible gateway is in front.
func reasoningExtra(fields map[string]respjson.Field) string {
	if s := extraString(fields, "reasoning_content"); s != "" {
		return s
	}
	return extraString(fields, "reasoning")
}

// extraString returns a string-valued extra (non-schema) delta field, or "".
func extraString(fields map[string]respjson.Field, key string) string {
	f, ok := fields[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal([]byte(f.Raw()), &s) == nil {
		return s
	}
	return ""
}

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// thinkSplitter is a streaming state machine that separates <think>…</think>
// reasoning from answer text. Tags may straddle chunk boundaries, so a partial
// tag prefix is carried to the next feed.
type thinkSplitter struct {
	inThink bool
	carry   string
}

// feed splits one content chunk into ordered text/thinking events.
func (ts *thinkSplitter) feed(s string) []llm.StreamEvent {
	data := ts.carry + s
	ts.carry = ""
	var evs []llm.StreamEvent
	for len(data) > 0 {
		marker := thinkOpen
		if ts.inThink {
			marker = thinkClose
		}
		if idx := strings.Index(data, marker); idx >= 0 {
			if before := data[:idx]; before != "" {
				evs = append(evs, ts.emit(before))
			}
			ts.inThink = !ts.inThink
			data = data[idx+len(marker):]
			continue
		}
		// No full marker. The tail might be a partial marker prefix — carry it so
		// the tag can complete on the next chunk; emit the rest.
		if cut := partialTailLen(data, marker); cut > 0 {
			ts.carry = data[len(data)-cut:]
			data = data[:len(data)-cut]
		}
		if data != "" {
			evs = append(evs, ts.emit(data))
		}
		break
	}
	return evs
}

// flush emits any carried partial tail at stream end (so nothing is lost).
func (ts *thinkSplitter) flush() []llm.StreamEvent {
	if ts.carry == "" {
		return nil
	}
	ev := ts.emit(ts.carry)
	ts.carry = ""
	return []llm.StreamEvent{ev}
}

func (ts *thinkSplitter) emit(s string) llm.StreamEvent {
	if ts.inThink {
		return llm.StreamEvent{DeltaThinking: s}
	}
	return llm.StreamEvent{DeltaText: s}
}

// partialTailLen returns how many trailing bytes of data are a proper prefix of
// marker (a possibly-incomplete tag at the chunk boundary).
func partialTailLen(data, marker string) int {
	n := len(marker) - 1
	if n > len(data) {
		n = len(data)
	}
	for ; n > 0; n-- {
		if strings.HasPrefix(marker, data[len(data)-n:]) {
			return n
		}
	}
	return 0
}
