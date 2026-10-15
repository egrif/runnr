package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var errNoTerminal = errors.New("no terminal available here (use the CLI, or the GUI on your own machine)")

// inTerminal runs command with shell attached to the operator's terminal,
// or in a new terminal window when the interactor can open one. An empty
// command opens an interactive shell ($SHELL). extra is runnr's
// environment on top of the process environment. The error is only for
// failing to run; a non-zero exit is reported through the exit code.
func (x *executor) inTerminal(ctx context.Context, title, shell, command string, extra []string, dir string) (*int, error) {
	if a, ok := x.ui.(Attacher); ok {
		var cmd *exec.Cmd
		if command == "" {
			cmd = exec.CommandContext(ctx, userShell(x.rb.Shell))
		} else {
			cmd = exec.CommandContext(ctx, shell, "-c", command)
		}
		cmd.Env, cmd.Dir = append(os.Environ(), extra...), dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = a.AttachedIO()
		err := cmd.Run()
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			if ctx.Err() != nil {
				return &code, ctx.Err()
			}
			return &code, nil
		}
		return nil, err
	}
	if w, ok := x.ui.(WindowLauncher); ok && w.LaunchWindows() {
		return launchWindow(ctx, title, shell, command, extra, dir)
	}
	return nil, errNoTerminal
}

func userShell(fallback string) string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if fallback != "" {
		return fallback
	}
	return "sh"
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// launchWindow runs command (or an interactive shell) in a new terminal
// window and waits for it to finish. The environment, which may hold
// secrets, goes through a 0600 file that's removed afterwards.
func launchWindow(ctx context.Context, title, shell, command string, extra []string, dir string) (*int, error) {
	tmp, err := os.MkdirTemp("", "runnr-term-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	var env strings.Builder
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok && identRe.MatchString(k) {
			fmt.Fprintf(&env, "export %s=%s\n", k, shQuote(v))
		}
	}
	envFile := filepath.Join(tmp, "env.sh")
	if err := os.WriteFile(envFile, []byte(env.String()), 0o600); err != nil {
		return nil, err
	}
	body := `"${SHELL:-/bin/sh}" -i`
	if command != "" {
		cmdFile := filepath.Join(tmp, "cmd")
		if err := os.WriteFile(cmdFile, []byte(command), 0o600); err != nil {
			return nil, err
		}
		body = shQuote(shell) + " " + shQuote(cmdFile)
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	done := filepath.Join(tmp, "done")
	script := fmt.Sprintf(`#!/bin/sh
printf '\033]0;%%s\007' %[1]s
cd %[2]s || exit 1
. %[3]s
printf '\033[1m── runnr: %%s\033[0m\n\n' %[1]s
%[4]s
code=$?
echo $code > %[5]s.tmp && mv %[5]s.tmp %[5]s
printf '\n\033[2m── runnr: finished (exit %%s). You can close this window.\033[0m\n' "$code"
`, shQuote("runnr: "+title), shQuote(dir), shQuote(envFile), body, shQuote(done))
	path := filepath.Join(tmp, "runnr.command")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return nil, err
	}

	var open *exec.Cmd
	switch {
	case os.Getenv("RUNNR_TERMINAL") != "":
		open = exec.Command("sh", "-c", os.Getenv("RUNNR_TERMINAL")+` "$1"`, "sh", path)
	case runtime.GOOS == "darwin":
		app := os.Getenv("RUNNR_TERMINAL_APP")
		if app == "" {
			app = "Terminal"
		}
		open = exec.Command("open", "-a", app, path)
	default:
		open = exec.Command("x-terminal-emulator", "-e", path)
	}
	if out, err := open.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("opening a terminal window: %v %s", err, strings.TrimSpace(string(out)))
	}

	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			b, err := os.ReadFile(done)
			if err != nil {
				continue
			}
			code, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				return nil, fmt.Errorf("terminal window reported %q", b)
			}
			return &code, nil
		}
	}
}
