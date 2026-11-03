//go:build darwin || linux || windows

package sandboxfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/adk/filesystem"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupWalkContinuesBeyondSearchLimit(t *testing.T) {
	b, err := NewBackend(context.Background(), &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// Exceed the search limit, with a directory spread to exercise recursive traversal.
	for i := 0; i < maxWalkEntries+1; i++ {
		name := filepath.Join(b.workspace, fmt.Sprintf("%05d", i))
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(b.workspace, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.workspace, "nested", "child"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("simulated ACL failure")
	visited := 0
	err = b.walkCleanup(context.Background(), func(string) error {
		visited++
		if visited == 1 {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || visited != maxWalkEntries+3 {
		t.Fatalf("visited=%d error=%v", visited, err)
	}
}

func TestCleanupWalkSkipsLinksAndHonorsCancellation(t *testing.T) {
	b, err := NewBackend(context.Background(), &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(b.workspace, "escape")); err == nil {
		if err := b.walkCleanup(context.Background(), func(p string) error { t.Fatalf("visited external link %s", p); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.walkCleanup(ctx, func(string) error { t.Fatal("visited after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestInvalidCommandRejectedBeforeValidation(t *testing.T) {
	b, err := NewBackend(context.Background(), &Config{Workspace: t.TempDir(), ValidateCommand: func(string) error { t.Fatal("invalid command reached validation"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, command := range []string{"", " \n", "echo bad\x00"} {
		_, err := b.ExecuteStreaming(context.Background(), &filesystem.ExecuteRequest{Command: command})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("%q: %v", command, err)
		}
	}
}
