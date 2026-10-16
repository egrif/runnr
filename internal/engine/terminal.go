package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

var errNoTerminal = errors.New("no terminal available here (run unattended steps from the CLI or the GUI)")

// TermHost is implemented by interactors that can give the operator a
// terminal: the CLI (its own) or the GUI (a terminal in the browser).
type TermHost interface {
	// StartTerm opens a terminal session for step idx (-1: none).
	StartTerm(idx int, title string) (TermConn, error)
}

// TermConn connects a pseudo-terminal to the operator: Read returns what
// they type, Write shows output. Read returning EOF means the operator
// ended the session: the command is hung up on, as when a terminal window
// is closed. Close ends the session; Read must return once it's closed.
type TermConn interface {
	io.ReadWriteCloser
	// Size returns the operator's terminal size.
	Size() (rows, cols uint16)
	// Resized signals size changes (may be nil).
	Resized() <-chan struct{}
}

// inTerminal runs command with shell on a pseudo-terminal connected to the
// operator, or an interactive shell ($SHELL) if command is "". The operator
// sees and types as in any terminal; the output is also returned, cleaned
// of terminal control codes, as a transcript. The error is only for
// failing to run; a non-zero exit is reported through the exit code.
func (x *executor) inTerminal(ctx context.Context, idx int, title, shell, command string, extra []string, dir string) (*int, string, error) {
	h, ok := x.ui.(TermHost)
	if !ok {
		return nil, "", errNoTerminal
	}
	var cmd *exec.Cmd
	if command == "" {
		cmd = exec.Command(userShell(x.rb.Shell))
	} else {
		cmd = exec.Command(shell, "-c", command)
	}
	cmd.Env, cmd.Dir = append(os.Environ(), append(extra, "TERM="+termName())...), dir

	conn, err := h.StartTerm(idx, title)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	rows, cols := conn.Size()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, "", err
	}

	// Keystrokes in. When the operator ends the session, hang up.
	var exited atomic.Bool
	go func() {
		_, _ = io.Copy(f, conn)
		if !exited.Load() {
			hangup(cmd.Process)
		}
	}()
	// Resizes go to the pty until the session ends; the resize goroutine
	// must be done before the pty is closed.
	ended := make(chan struct{})
	var resizing sync.WaitGroup
	defer func() {
		close(ended)
		resizing.Wait()
		f.Close()
	}()
	if ch := conn.Resized(); ch != nil {
		resizing.Add(1)
		go func() {
			defer resizing.Done()
			for {
				select {
				case <-ch:
					r, c := conn.Size()
					_ = pty.Setsize(f, &pty.Winsize{Rows: r, Cols: c})
				case <-ended:
					return
				}
			}
		}()
	}
	// Cancelling the run (or a timeout) kills the command.
	stop := context.AfterFunc(ctx, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer stop()

	// Output out, and into the transcript.
	rec := &ringBuffer{}
	_, _ = io.Copy(io.MultiWriter(conn, rec), f) // ends when the terminal closes (EIO)
	err = cmd.Wait()
	exited.Store(true)
	transcript := CleanTranscript(rec.String())
	if cmd.ProcessState == nil {
		return nil, transcript, err
	}
	code := cmd.ProcessState.ExitCode()
	if ctx.Err() != nil {
		return &code, transcript, ctx.Err()
	}
	return &code, transcript, nil
}

func termName() string {
	if t := os.Getenv("TERM"); t != "" && t != "dumb" {
		return t
	}
	return "xterm-256color"
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

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?<=>!]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78DEHMNOZc]`)
	ctrlRe = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
)

// CleanTranscript turns raw terminal output into plain text: control
// sequences removed, and for lines redrawn with carriage returns (progress
// bars, spinners) only the final version kept.
func CleanTranscript(raw string) string {
	s := ansiRe.ReplaceAllString(raw, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if j := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); j >= 0 {
			l = l[j+1:]
		}
		// Apply backspaces, then drop other control characters.
		var b []rune
		for _, r := range strings.TrimRight(l, "\r") {
			if r == '\b' {
				if len(b) > 0 {
					b = b[:len(b)-1]
				}
				continue
			}
			b = append(b, r)
		}
		lines[i] = ctrlRe.ReplaceAllString(string(b), "")
	}
	return strings.Join(lines, "\n")
}

// PipeConn is a TermConn for interactors whose operator is elsewhere (the
// browser): input arrives through Input, output goes to the out function.
type PipeConn struct {
	out    func([]byte)
	in     chan []byte
	resize chan struct{}
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	rows   uint16
	cols   uint16
	buf    []byte
}

func NewPipeConn(out func([]byte), rows, cols uint16) *PipeConn {
	return &PipeConn{out: out, in: make(chan []byte, 256), resize: make(chan struct{}, 1), done: make(chan struct{}), rows: rows, cols: cols}
}

// Input delivers keystrokes; it drops them once the session has ended.
func (p *PipeConn) Input(b []byte) {
	select {
	case p.in <- b:
	case <-p.done:
	case <-time.After(time.Second):
	}
}

// SetSize records a new terminal size.
func (p *PipeConn) SetSize(rows, cols uint16) {
	if rows == 0 || cols == 0 {
		return
	}
	p.mu.Lock()
	p.rows, p.cols = rows, cols
	p.mu.Unlock()
	select {
	case p.resize <- struct{}{}:
	default:
	}
}

func (p *PipeConn) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		select {
		case p.buf = <-p.in:
		case <-p.done:
			return 0, io.EOF
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *PipeConn) Write(b []byte) (int, error) {
	p.out(append([]byte(nil), b...))
	return len(b), nil
}

func (p *PipeConn) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *PipeConn) Size() (uint16, uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rows, p.cols
}

func (p *PipeConn) Resized() <-chan struct{} { return p.resize }

// Done is closed when the session ends.
func (p *PipeConn) Done() <-chan struct{} { return p.done }
