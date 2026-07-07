package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// The ticket is the one-time handover token that lets the re-exec'd plexus tell it
// is ALREADY inside the sandbox (so it self-confines instead of re-entering bwrap in
// a loop). It is NOT a security boundary: the parent controls both the env var and
// the filesystem, so a malicious parent could always forge one. The nonce check
// below only guards against a stale/mismatched env var or an accidental collision —
// the file's content must equal the random nonce embedded in its own name.

const ticketPrefix = "plexus_sandbox_"

// GenerateTicket writes a random nonce to a temp file named after that nonce and
// returns the path.
func GenerateTicket() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto rand failed: %w", err)
	}
	nonce := hex.EncodeToString(b)
	path := filepath.Join(os.TempDir(), ticketPrefix+nonce+".ticket")
	if err := os.WriteFile(path, []byte(nonce), 0600); err != nil {
		return "", fmt.Errorf("failed to write sandbox ticket to %s: %w", path, err)
	}
	return path, nil
}

// VerifyAndConsumeTicket reads the ticket, checks its content matches the nonce in
// its filename, then best-effort deletes it. See the package note: the content check
// is the guard (against a stale env var, not a hostile parent); the delete is only
// hygiene.
func VerifyAndConsumeTicket(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read sandbox ticket (spoof attempt or stale env?): %w", err)
	}
	name := filepath.Base(path)
	expected := strings.TrimSuffix(strings.TrimPrefix(name, ticketPrefix), ".ticket")
	if expected == "" || name == expected || string(data) != expected {
		return fmt.Errorf("sandbox ticket content mismatch (spoof attempt or stale env?)")
	}
	// Deletion is best-effort, NOT a failure condition. The jail stage delivers the
	// ticket by bind-mounting it into the sandbox (bwrap --bind), so from inside the
	// sandbox the ticket is a mountpoint and cannot be unlinked (EBUSY) — expected, not
	// an error. The nonce check above is what actually guards entry; the host /tmp copy
	// is a tiny file the OS reaper clears. (On a delivery that leaves it a plain file,
	// the remove still succeeds and gives one-shot semantics.)
	if err := os.Remove(path); err != nil {
		slog.Debug("sandbox ticket not unlinked from inside the sandbox (bind-mounted)", "path", path, "err", err)
	}
	return nil
}
