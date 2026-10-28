//go:build unix

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The CLI as an operator uses it: runnr built once, run in a terminal
// (a pty), its prompts answered as they come.

var runnrBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "runnr-cli-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runnrBin = filepath.Join(dir, "runnr")
	args := []string{"build", "-o", runnrBin}
	if os.Getenv("GOCOVERDIR") != "" {
		// Measuring coverage: runnr writes its own there as it runs.
		args = append(args, "-cover", "-coverpkg=github.com/egrif/runnr/...")
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), append(args, "github.com/egrif/runnr/cmd/runnr")...)
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building runnr: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// cliHome is a runnr home of its own, and a HOME and work folder for the
// commands it runs.
type cliHome struct {
	t    *testing.T
	dir  string // HOME, and the folder commands run in
	home string // runnr's home
}

func newCLIHome(t *testing.T) *cliHome {
	t.Helper()
	d := t.TempDir()
	return &cliHome{t: t, dir: d, home: filepath.Join(d, "runnr")}
}

func (h *cliHome) cmd(args ...string) *exec.Cmd {
	c := exec.Command(runnrBin, append([]string{"--home", h.home}, args...)...)
	c.Dir = h.dir
	c.Env = append(os.Environ(), "HOME="+h.dir, "USER=tester", "NO_COLOR=", "TERM=xterm", "RUNNR_HOME=", "RUNNR_DATABASE_URL=", "TMUX=")
	return c
}

// run runs a command to the end, without a terminal: its output, and
// whether it succeeded.
func (h *cliHome) run(args ...string) (string, error) {
	h.t.Helper()
	out, err := h.cmd(args...).CombinedOutput()
	return string(out), err
}

// must is run, failing the test if the command fails.
func (h *cliHome) must(args ...string) string {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("runnr %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// runbook imports a runbook (YAML) under its name.
func (h *cliHome) runbook(yaml string) {
	h.t.Helper()
	f := filepath.Join(h.dir, fmt.Sprintf("rb-%d.yaml", time.Now().UnixNano()))
	if err := os.WriteFile(f, []byte(yaml), 0o644); err != nil {
		h.t.Fatal(err)
	}
	h.must("import", f)
}

// session is a command running in a terminal.
type session struct {
	t    *testing.T
	cmd  *exec.Cmd
	tty  *os.File
	mu   sync.Mutex
	out  bytes.Buffer
	seen int // how far expect has read
	done chan struct{}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\r`)

func (h *cliHome) start(args ...string) *session {
	h.t.Helper()
	c := h.cmd(args...)
	tty, err := pty.StartWithSize(c, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		h.t.Fatal(err)
	}
	s := &session{t: h.t, cmd: c, tty: tty, done: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				close(s.done)
				return
			}
		}
	}()
	h.t.Cleanup(func() {
		_ = c.Process.Kill()
		_ = c.Wait()
		tty.Close()
	})
	return s
}

// text is everything the terminal showed, colours left out.
func (s *session) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ansi.ReplaceAllString(s.out.String(), "")
}

// expect waits for text to appear after what expect last found, and
// moves past it.
func (s *session) expect(want string) {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		all := s.text()
		if i := strings.Index(all[min(s.seen, len(all)):], want); i >= 0 {
			s.seen += i + len(want)
			return
		}
		select {
		case <-s.done:
			if all := s.text(); strings.Contains(all[min(s.seen, len(all)):], want) {
				continue
			}
			s.t.Fatalf("runnr ended before %q; it showed:\n%s", want, s.text())
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no %q after 20s; it showed:\n%s", want, s.text())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// send types a line.
func (s *session) send(line string) {
	s.t.Helper()
	if _, err := s.tty.Write([]byte(line + "\r")); err != nil {
		s.t.Fatal(err)
	}
}

// answer waits for a prompt and types a line.
func (s *session) answer(prompt, line string) {
	s.t.Helper()
	s.expect(prompt)
	s.send(line)
}

// wait waits for runnr to end and returns its exit code.
func (s *session) wait() int {
	s.t.Helper()
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		s.t.Fatalf("runnr didn't end; it showed:\n%s", s.text())
	}
	err := s.cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return 0
}

// notSeen fails if text appears anywhere in what the terminal showed.
func (s *session) notSeen(text string) {
	s.t.Helper()
	if strings.Contains(s.text(), text) {
		s.t.Fatalf("showed %q:\n%s", text, s.text())
	}
}
