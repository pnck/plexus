package sandbox

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

// GenerateTicket generates a random 16-byte hex ticket and writes "OK" to a temp file.
// It returns the absolute path to the ticket file.
func GenerateTicket() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto rand failed: %w", err)
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("plexus_sandbox_%x.ticket", b))

	if err := os.WriteFile(path, []byte("OK"), 0600); err != nil {
		return "", fmt.Errorf("failed to write sandbox ticket to %s: %w", path, err)
	}

	return path, nil
}

// VerifyAndConsumeTicket reads the specified ticket file and then immediately deletes it.
func VerifyAndConsumeTicket(path string) error {
	_, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read sandbox ticket (spoof attempt or stale env?): %w", err)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to consume/delete sandbox ticket: %w", err)
	}

	return nil
}
