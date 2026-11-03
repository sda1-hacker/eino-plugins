//go:build darwin || linux || windows

package sandboxfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
)

func TestEditRejectsExpansionBeforeAllocation(t *testing.T) {
	b, err := NewBackend(context.Background(), &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p := filepath.Join(b.workspace, "file.txt")
	original := strings.Repeat("a", 64*1024)
	if err := os.WriteFile(p, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Repeat("b", 1024)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = b.Edit(context.Background(), &filesystem.EditRequest{FilePath: p, OldString: "a", NewString: replacement, ReplaceAll: true})
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected size rejection: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("oversized replacement allocated %d bytes", allocated)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != original {
		t.Fatalf("rejected edit changed file: %v", err)
	}
	// An exact-limit replacement succeeds, and a rejected write preserves it.
	if err := b.Edit(context.Background(), &filesystem.EditRequest{FilePath: p, OldString: original, NewString: strings.Repeat("b", maxFileBytes)}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(context.Background(), &filesystem.WriteRequest{FilePath: p, Content: strings.Repeat("c", maxFileBytes+1)}); !errors.Is(err, ErrDenied) {
		t.Fatalf("oversized write: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() != maxFileBytes {
		t.Fatalf("boundary write: %v %v", info, err)
	}
}

func TestGrepGlobRelativeToSearchRoot(t *testing.T) {
	ctx := context.Background()
	b, err := NewBackend(ctx, &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p := filepath.Join(b.workspace, "sub", "nested", "a.go")
	if err := b.Write(ctx, &filesystem.WriteRequest{FilePath: p, Content: "needle"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ base, glob string }{
		{filepath.Join(b.workspace, "sub"), "nested/*.go"},
		{b.workspace, "sub/nested/*.go"},
		{filepath.Join(b.workspace, "sub"), "*.go"},
		{p, "*.go"},
	} {
		got, err := b.GrepRaw(ctx, &filesystem.GrepRequest{Path: tc.base, Glob: tc.glob, Pattern: "needle"})
		if err != nil || len(got) != 1 || got[0].Path != p {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
}

func TestStreamBackpressureCleanupSmoke(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("checks macOS host temporary-directory cleanup")
	}
	b := smokeBackend(t, 700*time.Millisecond)
	sr, err := b.ExecuteStreaming(context.Background(), &filesystem.ExecuteRequest{Command: `printf '%s\n' "$TMPDIR"; sleep 0.1; /usr/bin/awk 'BEGIN { for (i=0;i<32768;i++) print "0123456789abcdef" }'; sleep 10`})
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	first, err := sr.Recv()
	if err != nil {
		t.Fatal(err)
	}
	temp := strings.TrimSpace(first.Output)
	if !filepath.IsAbs(temp) {
		t.Fatalf("missing private temp path: %q", first.Output)
	}
	// Do not consume or close the stream until after the timeout and cleanup.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := os.Stat(temp)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temporary directory survives timeout with paused consumer: %s (%v)", temp, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := collectExecuteStream(sr); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline event lost: %v", err)
	}
}

func TestStreamCoalescedOutputSmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses system Perl")
	}
	b := smokeBackend(t, 5*time.Second)
	if _, err := os.Stat("/usr/bin/perl"); err != nil {
		t.Skip("requires system Perl")
	}
	sr, err := b.ExecuteStreaming(context.Background(), &filesystem.ExecuteRequest{Command: `/usr/bin/perl -e '$|=1; for (1..600) { print "x"; select undef, undef, undef, 0.002; }'`})
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	// Pause until the producer has emitted more than maxStreamChunks small writes.
	time.Sleep(2 * time.Second)
	got, err := collectExecuteStream(sr)
	if err != nil || got.ExitCode == nil || *got.ExitCode != 0 || got.Truncated || got.Output != strings.Repeat("x", 600) {
		t.Fatalf("coalesced stream lost output: %+v %v", got, err)
	}
}
