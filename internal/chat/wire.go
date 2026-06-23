package chat

import "encoding/json"

// Chat-internal wire protocol over the bus. The user and the hosted agent are
// control-plane peers; everything between them — user turns, approval answers,
// control commands, streamed replies — rides one structured frame carried in the
// message Payload. plexus's envelope is unchanged; only this dev REPL puts a
// frame inside the payload. CorrelationID still pairs an approval answer/reply to
// its request (§5.7.5 synchronous form).
type Frame struct {
	Kind string `json:"k"`           // see kinds below
	Cmd  string `json:"c,omitempty"` // ctrl: command name
	Arg  string `json:"a,omitempty"` // ctrl: argument
	Text string `json:"t,omitempty"` // text payload (turn text / chunk / result / desc)
	Done bool   `json:"d,omitempty"` // terminates a streamed reply
}

// Frame kinds. client→host: say / answer / ctrl. host→client: delta / reply /
// usage / approval / ctrl / error.
const (
	kindSay      = "say"      // user turn (Text = message)
	kindAnswer   = "answer"   // approval answer (Text = approve|deny)
	kindCancel   = "cancel"   // Ctrl-C: reset the in-flight turn (no payload)
	kindCtrl     = "ctrl"     // control command (host) / control result (back)
	kindDelta    = "delta"    // streamed assistant text chunk (Text = chunk)
	kindReply    = "reply"    // turn finished (Done=true; Text = full reply)
	kindUsage    = "usage"    // token usage line (Text = formatted)
	kindApproval = "approval" // approval request (Text = description)
	kindError    = "error"    // turn-level error (Text = message)
	kindTrace    = "trace"    // tool/delegation dispatch trace (Cmd=name, Arg=args, Text=result)
)

const (
	approveWord = "approve"
	denyWord    = "deny"
)

// Control command names (Frame.Cmd) the host understands.
const (
	cmdKey       = "key"
	cmdProvider  = "provider"
	cmdModel     = "model"
	cmdModels    = "models"
	cmdSystem    = "system"
	cmdReset     = "reset"
	cmdStatus    = "status"
	cmdDebug     = "debug"
	cmdTools     = "tools"
	cmdSteps     = "steps"
	cmdMemory    = "memory"
	cmdReasoning = "reasoning"
)

func encodeFrame(f Frame) []byte {
	b, _ := json.Marshal(f) // Frame is always marshalable
	return b
}

// decodeFrame parses a payload as a Frame; ok is false if it is not one (so a
// raw/foreign message can fall back to being treated as plain text).
func decodeFrame(b []byte) (Frame, bool) {
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil || f.Kind == "" {
		return Frame{}, false
	}
	return f, true
}
