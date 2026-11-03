//go:build darwin || linux

package sandboxfs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLauncherHandshake(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		unavailable  bool
	}{
		{"launcher rejects policy", "exit 1", true},
		{"unexpected success without sandbox", "exit 0", true},
		{"command nonzero after readiness", unixShellReady, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			cmd := execCommand{Cmd: exec.CommandContext(ctx, "/bin/sh", "-c", tc.script, "sandboxfs", "exit 7"), ctx: ctx}
			err = cmd.Start(out)
			if tc.unavailable {
				if !errors.Is(err, ErrShellUnavailable) {
					t.Fatalf("expected unavailable: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := commandResult(cmd.Wait())
			if err != nil || result.ExitCode == nil || *result.ExitCode != 7 {
				t.Fatalf("user exit lost: %+v %v", result, err)
			}
		})
	}
}
