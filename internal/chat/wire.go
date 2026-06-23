package chat

import "strings"

// Chat-internal wire conventions over the bus. plexus has no dedicated approval
// message type, and we do not change the protocol envelope for a dev REPL — so
// chat marks an approval-request report with a payload prefix the client strips,
// and the user's answer is a plain message carrying the request's CorrelationID
// (the host demuxes it back to the waiting approver, §5.7.5 synchronous form).

// approvalRequestPrefix marks a report as an approval request (vs a normal
// reply). The client shows the remainder and enters /approve–/deny mode.
const approvalRequestPrefix = "\x01approval\x01"

// approveWord / denyWord are the answer payloads the client sends.
const (
	approveWord = "approve"
	denyWord    = "deny"
)

// isApproveAnswer reports whether a payload is an approval (vs denial).
func isApproveAnswer(payload []byte) bool {
	return strings.EqualFold(strings.TrimSpace(string(payload)), approveWord)
}

// markApprovalRequest wraps a human-readable description as an approval-request
// payload.
func markApprovalRequest(desc string) string { return approvalRequestPrefix + desc }

// parseApprovalRequest returns the description and true if payload is an approval
// request.
func parseApprovalRequest(payload string) (string, bool) {
	if strings.HasPrefix(payload, approvalRequestPrefix) {
		return strings.TrimPrefix(payload, approvalRequestPrefix), true
	}
	return "", false
}
