package bwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ExtractBwrap writes the embedded bwrap binary to a temporary executable file.
// It returns the absolute path to the extracted binary.
// The caller is responsible for cleaning it up, or leaving it if it's placed in a stable cache location.
func ExtractBwrap() (string, error) {
	if len(bwrapBinary) == 0 {
		return "", fmt.Errorf("sandboxed mode is not supported on this OS/Arch combination (bwrap binary not embedded)")
	}
	// For now, we extract to the system temp directory.
	// In a real production scenario, this might go to ~/.plexus/cache/bwrap
	tmpFile, err := os.CreateTemp("", "plexus-bwrap-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for bwrap: %w", err)
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(bwrapBinary); err != nil {
		return "", fmt.Errorf("failed to write embedded bwrap: %w", err)
	}

	// Make the file executable
	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		return "", fmt.Errorf("failed to chmod bwrap binary: %w", err)
	}

	return filepath.Abs(tmpFile.Name())
}

type Provider struct{}

func New() *Provider {
	return &Provider{}
}

func (p *Provider) Name() string {
	return "bwrap"
}

// Enter extracts bwrap, constructs the isolation arguments, and performs syscall.Exec.
func (p *Provider) Enter(ticketPath string, extraArgs []string) error {
	bwrapPath, err := ExtractBwrap()
	if err != nil {
		return fmt.Errorf("failed to extract embedded bwrap: %w", err)
	}

	bwrapArgs := []string{
		bwrapPath,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--unshare-all",
		"--share-net",
		"--bind", ticketPath, ticketPath,
	}

	if len(extraArgs) > 0 {
		bwrapArgs = append(bwrapArgs, extraArgs...)
	}

	bwrapArgs = append(bwrapArgs, "--")
	bwrapArgs = append(bwrapArgs, os.Args...)

	return syscall.Exec(bwrapPath, bwrapArgs, os.Environ())
}
