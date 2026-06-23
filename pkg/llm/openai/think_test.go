package openai

import (
	"strings"
	"testing"

	"plexus/pkg/llm"
)

// feed a sequence of chunks through one splitter and concatenate the resulting
// thinking vs answer text.
func runSplitter(chunks ...string) (thinking, answer string) {
	var ts thinkSplitter
	emit := func(evs []llm.StreamEvent) {
		for _, e := range evs {
			thinking += e.DeltaThinking
			answer += e.DeltaText
		}
	}
	for _, c := range chunks {
		emit(ts.feed(c))
	}
	emit(ts.flush())
	return
}

func TestThinkSplitterWholeTags(t *testing.T) {
	think, ans := runSplitter("<think>reasoning here</think>the answer")
	if think != "reasoning here" {
		t.Fatalf("thinking = %q, want 'reasoning here'", think)
	}
	if ans != "the answer" {
		t.Fatalf("answer = %q, want 'the answer'", ans)
	}
}

func TestThinkSplitterTagsSplitAcrossChunks(t *testing.T) {
	// Tags broken at every awkward point.
	think, ans := runSplitter("<thi", "nk>plan", " more", "</thi", "nk>", "ans", "wer")
	if think != "plan more" {
		t.Fatalf("thinking = %q, want 'plan more'", think)
	}
	if ans != "answer" {
		t.Fatalf("answer = %q, want 'answer'", ans)
	}
}

func TestThinkSplitterNoTagsIsPassthrough(t *testing.T) {
	think, ans := runSplitter("just ", "a normal ", "answer")
	if think != "" {
		t.Fatalf("thinking = %q, want empty", think)
	}
	if ans != "just a normal answer" {
		t.Fatalf("answer = %q, want passthrough", ans)
	}
}

func TestThinkSplitterUnclosedFlushesAsThinking(t *testing.T) {
	// Model truncated mid-thinking — the open think runs to the end.
	think, ans := runSplitter("<think>still thinking when cut o", "ff")
	if think != "still thinking when cut off" {
		t.Fatalf("thinking = %q, want full thinking", think)
	}
	if ans != "" {
		t.Fatalf("answer = %q, want empty", ans)
	}
}

func TestThinkSplitterTextBeforeThink(t *testing.T) {
	think, ans := runSplitter("preamble <think>hmm</think> done")
	if think != "hmm" {
		t.Fatalf("thinking = %q", think)
	}
	if ans != "preamble  done" {
		t.Fatalf("answer = %q, want 'preamble  done'", ans)
	}
}

// A '<' that is not a think tag must not be swallowed.
func TestThinkSplitterBareAngleBracket(t *testing.T) {
	_, ans := runSplitter("if a < b then", " x")
	if ans != "if a < b then x" {
		t.Fatalf("answer = %q, want the '<' preserved", ans)
	}
	if strings.Contains(ans, "think") {
		t.Fatal("unexpected think handling")
	}
}
