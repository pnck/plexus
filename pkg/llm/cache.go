package llm

// CacheBreakpoints returns the message indices a provider should mark as
// prompt-cache write points (ascending). It is the provider-NEUTRAL caching
// policy; each provider translates the indices to its own API — Anthropic stamps
// an explicit cache_control breakpoint on that message's last block, while OpenAI
// caches prefixes automatically and only uses the indices to scope a stable
// prompt_cache_key. Keeping the policy here means both backends cache the same
// prefixes regardless of how each expresses it.
//
// Policy (deliberately coarse — see the TODO): cache up to TWO prefixes.
//
//  1. The stable system prefix (kernel principles + role card): the LAST system
//     message. It is constant across a session, so this is a permanent cache hit.
//  2. A ROLLING conversation prefix at len(msgs)-2, so each turn's prefix (minus
//     the freshest tail) is cached for the next turn. The breakpoint stays TWO
//     messages back from the end because the last ~2 messages are the ones most
//     likely to change within a couple of turns — a user re-edits or retries, or
//     an assistant turn is regenerated — and caching them would write an entry
//     that is immediately invalidated. A cached prefix only helps a request that
//     CONTAINS it, so this breakpoint must only ever move FORWARD as history grows.
//
// At most two breakpoints are returned, well under Anthropic's limit of four.
//
// TODO(cache-policy): make this content-adaptive. When the user signals
// satisfaction (no recent edits/retries) cache aggressively up to the last turn;
// under high churn stay conservative, or drop the rolling breakpoint entirely.
func CacheBreakpoints(msgs []Message) []int {
	lastSys := -1
	for i, m := range msgs {
		if m.Role == RoleSystem {
			lastSys = i
		}
	}
	var bps []int
	if lastSys >= 0 {
		bps = append(bps, lastSys)
	}
	// Rolling conversation breakpoint: held two messages back from the tail and
	// strictly past the system prefix (no point re-caching inside it).
	if roll := len(msgs) - 2; roll > lastSys {
		bps = append(bps, roll)
	}
	return bps
}
