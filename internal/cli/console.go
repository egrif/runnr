package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/egrif/runnr/internal/engine"
)

// keyboard is the only reader of stdin, so prompts and terminal sessions
// never compete for keystrokes (and nothing is left blocked on stdin when
// a session ends).
type keyboard struct {
	once    sync.Once
	ch      chan []byte
	mu      sync.Mutex
	pending []byte
}

var kbd keyboard

func (k *keyboard) start() {
	k.once.Do(func() {
		k.ch = make(chan []byte, 64)
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					k.ch <- append([]byte(nil), buf[:n]...)
				}
				if err != nil {
					close(k.ch)
					return
				}
			}
		}()
	})
}

// next returns the next chunk of input.
func (k *keyboard) next(ctx context.Context) ([]byte, error) {
	k.start()
	k.mu.Lock()
	if len(k.pending) > 0 {
		b := k.pending
		k.pending = nil
		k.mu.Unlock()
		return b, nil
	}
	k.mu.Unlock()
	select {
	case b, ok := <-k.ch:
		if !ok {
			return nil, io.EOF
		}
		if ctx.Err() != nil {
			k.unread(b)
			return nil, ctx.Err()
		}
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// unread puts input back for the next reader.
func (k *keyboard) unread(b []byte) {
	if len(b) == 0 {
		return
	}
	k.mu.Lock()
	k.pending = append(append([]byte(nil), b...), k.pending...)
	k.mu.Unlock()
}

func (t *Terminal) readLine(ctx context.Context) (string, error) {
	var line []byte
	for {
		b, err := kbd.next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					return "", engine.ErrAborted
				}
				return strings.TrimSpace(string(line)), nil
			}
			return "", err
		}
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line = append(line, b[:i]...)
			kbd.unread(b[i+1:])
			return strings.TrimSpace(string(line)), nil
		}
		line = append(line, b...)
	}
}

// readSecret reads a line without echoing it.
func (t *Terminal) readSecret(ctx context.Context) (string, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return t.readLine(ctx)
	}
	defer func() {
		_ = term.Restore(fd, old)
		fmt.Fprintln(t.out)
	}()
	var s []rune
	for {
		b, err := kbd.next(ctx)
		if err != nil {
			return "", err
		}
		for i, r := range string(b) {
			switch r {
			case '\r', '\n':
				kbd.unread(b[i+1:])
				return string(s), nil
			case 3: // Ctrl-C
				return "", engine.ErrAborted
			case 127, '\b':
				if len(s) > 0 {
					s = s[:len(s)-1]
				}
			default:
				s = append(s, r)
			}
		}
	}
}

// StartTerm gives a terminal session the real terminal (engine.TermHost):
// raw mode, so every key (Ctrl-C included) goes to the command.
func (t *Terminal) StartTerm(idx int, title string) (engine.TermConn, error) {
	if !isTTY(os.Stdin) {
		return nil, errors.New("stdin is not a terminal")
	}
	fmt.Fprintln(t.out, t.c("2", "  ┌─ "+title+" · you're connected; output is recorded"))
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &cliConn{t: t, fd: fd, old: old, ctx: ctx, cancel: cancel, resize: make(chan struct{}, 1), winch: make(chan os.Signal, 1),
		release: t.interrupts.hold("(Ctrl-C went to the step's command, which has ended)")}
	notifyResize(c.winch)
	go func() {
		for {
			select {
			case <-c.winch:
				select {
				case c.resize <- struct{}{}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return c, nil
}

type cliConn struct {
	t      *Terminal
	fd     int
	old    *term.State
	ctx    context.Context
	cancel context.CancelFunc
	resize chan struct{}
	winch  chan os.Signal
	once   sync.Once
	// release gives Ctrl-C back to runnr: a moment after the session
	// ends, so one meant for the command that lands just after it ended
	// doesn't abort the run.
	release func()
}

// ttyGrace is how long after a terminal session ends Ctrl-C still doesn't
// abort the run.
const ttyGrace = 2 * time.Second

func (c *cliConn) Read(b []byte) (int, error) {
	chunk, err := kbd.next(c.ctx)
	if err != nil {
		return 0, io.EOF
	}
	n := copy(b, chunk)
	kbd.unread(chunk[n:])
	return n, nil
}

func (c *cliConn) Write(b []byte) (int, error) { return os.Stdout.Write(b) }

func (c *cliConn) Close() error {
	c.once.Do(func() {
		c.cancel()
		signal.Stop(c.winch)
		_ = term.Restore(c.fd, c.old)
		fmt.Fprintln(c.t.out, c.t.c("2", "\r  └─ session ended"))
		time.AfterFunc(ttyGrace, c.release)
	})
	return nil
}

func (c *cliConn) Size() (uint16, uint16) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 24, 80
	}
	return uint16(h), uint16(w)
}

func (c *cliConn) Resized() <-chan struct{} { return c.resize }
