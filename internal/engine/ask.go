package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// A step can ask the operator a question in the middle of its script:
// runnr_ask PROMPT (prints the answer) and runnr_confirm PROMPT (succeeds
// on yes) are defined in every step. On a terminal (tty steps) they read
// it; otherwise they send the question through a FIFO in the step's
// directory ($RUNNR_ASK) and runnr asks whoever is running it: the CLI's
// prompt, the GUI's question, a test's operator. Nobody to ask (an
// unattended run) is a failure, so the step doesn't carry on unanswered.

// StepAsker is implemented by interactors that can put a step's question
// to a person. kind is "text" or "confirm" (answered yes or no).
type StepAsker interface {
	AskStep(ctx context.Context, idx int, step runbook.Step, kind, prompt string) (string, error)
}

// askFunctions are the shell functions steps call (sh, bash and zsh).
const askFunctions = `
runnr_ask() {
  if [ -t 0 ] || [ -z "${RUNNR_ASK:-}" ]; then
    printf '%s ' "$1" >&2; IFS= read -r REPLY || return 1; printf '%s\n' "$REPLY"; return 0
  fi
  printf 'text\t%s\n' "$(printf '%s' "$1" | tr '\n\t' '  ')" > "$RUNNR_ASK/ask.req"
  IFS= read -r REPLY < "$RUNNR_ASK/ask.res"
  case $REPLY in
    ok"	"*) printf '%s\n' "${REPLY#ok	}" ;;
    *) printf 'runnr_ask: %s\n' "${REPLY#err	}" >&2; return 1 ;;
  esac
}
runnr_confirm() {
  if [ -t 0 ] || [ -z "${RUNNR_ASK:-}" ]; then
    printf '%s [y/N] ' "$1" >&2; IFS= read -r REPLY || return 1
  else
    printf 'confirm\t%s\n' "$(printf '%s' "$1" | tr '\n\t' '  ')" > "$RUNNR_ASK/ask.req"
    IFS= read -r REPLY < "$RUNNR_ASK/ask.res"
    case $REPLY in
      ok"	"*) REPLY=${REPLY#ok	} ;;
      *) printf 'runnr_confirm: %s\n' "${REPLY#err	}" >&2; return 1 ;;
    esac
  fi
  case $REPLY in [yY]*) return 0 ;; *) return 1 ;; esac
}
`

// serveAsk answers a step's runnr_ask/runnr_confirm questions until ctx
// ends. It makes the FIFOs in dir and returns the value for $RUNNR_ASK.
func (x *executor) serveAsk(ctx context.Context, i int, step runbook.Step, dir string) (string, error) {
	req, res := filepath.Join(dir, "ask.req"), filepath.Join(dir, "ask.res")
	for _, p := range []string{req, res} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			return "", err
		}
	}
	// Read-write, so opening doesn't wait for a writer, and reads block
	// (rather than end) between questions.
	rf, err := os.OpenFile(req, os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	go func() { <-ctx.Done(); rf.Close() }()
	go func() {
		sc := bufio.NewScanner(rf)
		for sc.Scan() {
			kind, prompt, _ := strings.Cut(sc.Text(), "\t")
			answer, err := x.askOperator(ctx, i, step, kind, prompt)
			line := "ok\t" + answer
			if err != nil {
				line = "err\t" + strings.ReplaceAll(err.Error(), "\n", " ")
			}
			// The step opens its end right after asking; give up if it ends first.
			wf := openWhenRead(ctx, res)
			if wf == nil {
				return
			}
			fmt.Fprintln(wf, line)
			wf.Close()
		}
	}()
	return dir, nil
}

// openWhenRead opens a FIFO for writing once its reader is there (nil if
// ctx ends first).
func openWhenRead(ctx context.Context, path string) *os.File {
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			// Writes block normally from here.
			if err := syscall.SetNonblock(int(f.Fd()), false); err != nil {
				f.Close()
				return nil
			}
			return f
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (x *executor) askOperator(ctx context.Context, i int, step runbook.Step, kind, prompt string) (string, error) {
	x.askMu.Lock()
	defer x.askMu.Unlock()
	x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "? " + x.mask(prompt)})
	a, ok := x.ui.(StepAsker)
	if !ok || !x.opts.Interactive {
		return "", errors.New("nobody to ask: this run is unattended")
	}
	q, done := x.asking(ctx)
	answer, err := a.AskStep(q, i, step, kind, prompt)
	done()
	if err != nil {
		return "", err
	}
	answer = strings.ReplaceAll(answer, "\n", " ")
	shown := answer
	if kind == "text" && answer == "" {
		shown = "(blank)"
	}
	x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "→ " + x.mask(shown)})
	return answer, nil
}
