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
	conn, err := h.StartTerm(idx, title)
	if err != nil {
		return nil, "", err
	}
	s, err := x.startTerm(ctx, shell, command, extra, dir, conn)
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	return s.wait()
}

// termSession is a command running on a pseudo-terminal.
type termSession struct {
	cmd        *exec.Cmd
	done       chan struct{}
	code       *int
	err        error
	transcript string
}

// startTerm starts command (or $SHELL if "") on a pty connected to conn and
// returns at once; wait for it with wait. conn is closed when it ends.
func (x *executor) startTerm(ctx context.Context, shell, command string, extra []string, dir string, conn TermConn) (*termSession, error) {
	var cmd *exec.Cmd
	cleanup := func() {}
	if command == "" {
		var shellEnv []string
		var err error
		if cmd, shellEnv, cleanup, err = x.interactiveShell(); err != nil {
			return nil, err
		}
		extra = append(extra, shellEnv...)
	} else {
		cmd = x.command(context.Background(), shell, "-c", command)
	}
	cmd.Env, cmd.Dir = append(x.baseEnv(), append(extra, "TERM="+termName())...), dir
	rows, cols := conn.Size()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		cleanup()
		return nil, err
	}
	s := &termSession{cmd: cmd, done: make(chan struct{})}

	// Keystrokes in. When the operator ends the session, hang up.
	var exited atomic.Bool
	go func() {
		_, _ = io.Copy(f, conn)
		if !exited.Load() {
			hangup(cmd.Process)
		}
	}()
	// Resizes go to the pty until the session ends.
	ended := make(chan struct{})
	var resizing sync.WaitGroup
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
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })

	go func() {
		defer close(s.done)
		rec := &ringBuffer{}
		_, _ = io.Copy(io.MultiWriter(conn, rec), f) // ends when the terminal closes (EIO)
		err := cmd.Wait()
		exited.Store(true)
		stop()
		close(ended)
		resizing.Wait()
		f.Close()
		conn.Close()
		cleanup()
		s.transcript = CleanTranscript(rec.String())
		if cmd.ProcessState == nil {
			s.err = err
			return
		}
		code := cmd.ProcessState.ExitCode()
		s.code = &code
		if ctx.Err() != nil {
			s.err = ctx.Err()
		}
	}()
	return s, nil
}

func (s *termSession) wait() (*int, string, error) {
	<-s.done
	return s.code, s.transcript, s.err
}

// stop hangs up the session (as closing its terminal would) and waits for
// it, killing it if it won't go.
func (s *termSession) stop() {
	select {
	case <-s.done:
		return
	default:
	}
	hangup(s.cmd.Process)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

// BackgroundHost is implemented by interactors that show background
// sessions (a tab in the GUI; attachable in the CLI). Without one, a
// background session just runs and is recorded.
type BackgroundHost interface {
	StartBackground(idx int, title string) TermConn
}

// BackgroundConn is a terminal nobody has to be watching: it keeps recent
// output so a viewer can attach later, then follow it live and type.
type BackgroundConn struct {
	*PipeConn
	mu   sync.Mutex
	raw  []byte
	subs map[chan []byte]struct{}
}

const backgroundKeep = 256 << 10

func NewBackgroundConn(rows, cols uint16) *BackgroundConn {
	b := &BackgroundConn{subs: map[chan []byte]struct{}{}}
	b.PipeConn = NewPipeConn(b.fanout, rows, cols)
	return b
}

func (b *BackgroundConn) fanout(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.raw = append(b.raw, p...)
	if len(b.raw) > backgroundKeep {
		b.raw = b.raw[len(b.raw)-backgroundKeep:]
	}
	for ch := range b.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// Attach returns recent output and a channel of what follows; call detach
// when done watching.
func (b *BackgroundConn) Attach() (recent []byte, live <-chan []byte, detach func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, 256)
	b.subs[ch] = struct{}{}
	return append([]byte(nil), b.raw...), ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func termName() string {
	if t := os.Getenv("TERM"); t != "" && t != "dumb" {
		return t
	}
	return "xterm-256color"
}

// interactiveShellName is the shell opened for the operator: the runbook's
// shell, so what's pasted from its steps behaves as it does in them, else
// their own $SHELL.
func interactiveShellName(runbookShell string) string {
	if runbookShell != "" {
		return runbookShell
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
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

// RunOnTerm runs a command (e.g. an editor) on a pty connected to conn and
// waits for it; conn is closed when it ends.
func RunOnTerm(ctx context.Context, conn TermConn, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "TERM="+termName())
	rows, cols := conn.Size()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		conn.Close()
		return err
	}
	go func() { _, _ = io.Copy(f, conn) }()
	ended := make(chan struct{})
	if ch := conn.Resized(); ch != nil {
		go func() {
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
	_, _ = io.Copy(conn, f)
	err = cmd.Wait()
	close(ended)
	f.Close()
	conn.Close()
	return err
}
