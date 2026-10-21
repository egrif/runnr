package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// A run with --tmux lives in a tmux session, so it outlives the terminal
// that started it (close the window, lose the ssh connection: runnr attach
// gets it back), and the operator sees it all at once:
//
//	┌──────────────────────────┬──────────────────┐
//	│ the run (prompts, output)│ runnr watch: the │
//	│                          │ steps, live      │
//	│                          ├──────────────────┤
//	│                          │ a shell with the │
//	│                          │ run's variables  │
//	└──────────────────────────┴──────────────────┘
//
// and a window for each background step, which runs outside runnr (in
// runnr __detached) so it survives runnr itself: a failed or aborted run
// leaves it running, and resuming the run follows it again.
//
// Outside tmux, sessions are on their own server (tmux -L runnr); inside
// tmux, on the current one, so runnr switches to them.

// storeFlags reach the store this runnr uses (for the panes it starts).
var storeFlags []string

const (
	envTmuxSession = "RUNNR_TMUX_SESSION"
	envTmuxSocket  = "RUNNR_TMUX_SOCKET" // "" when on the default server
	tmuxSocketName = "runnr"
)

// tmuxServer is how to reach the tmux server sessions live on.
type tmuxServer struct {
	Socket string `json:"socket,omitempty"` // -L name; "" for the default server
}

func (s tmuxServer) cmd(args ...string) *exec.Cmd {
	if s.Socket != "" {
		args = append([]string{"-L", s.Socket}, args...)
	}
	return exec.Command("tmux", args...)
}

func (s tmuxServer) run(args ...string) (string, error) {
	out, err := s.cmd(args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// currentTmuxServer: the one this runnr's session is on, else the one
// around this terminal, else runnr's own.
func currentTmuxServer() tmuxServer {
	if os.Getenv(envTmuxSession) != "" {
		return tmuxServer{Socket: os.Getenv(envTmuxSocket)}
	}
	if os.Getenv("TMUX") != "" {
		return tmuxServer{}
	}
	return tmuxServer{Socket: tmuxSocketName}
}

func needTmux() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return errors.New("--tmux needs tmux (brew install tmux)")
	}
	out, err := exec.Command("tmux", "-V").Output()
	if err == nil {
		if v := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(string(out)); v != nil {
			if major, _ := strconv.Atoi(v[1]); major < 3 {
				return fmt.Errorf("--tmux needs tmux 3.0 or later (this is %s)", strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

var sessionUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// startTmuxRun starts `runnr <args>` (a run or resume) in a new tmux
// session, with the run ID given, and attaches to it.
func startTmuxRun(args []string, runbookName, id string) error {
	if err := needTmux(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	srv := currentTmuxServer()
	name := "runnr-" + sessionUnsafe.ReplaceAllString(runbookName, "-") + "-" + id[len(id)-6:]
	pane := func(argv ...string) (string, error) {
		f, err := writeEnvFile()
		if err != nil {
			return "", err
		}
		return shellQuote(append([]string{exe, "__tmuxenv", f, "--"}, argv...)), nil
	}
	nav, err := pane(append(append([]string{exe}, args...), "--run-id", id)...)
	if err != nil {
		return err
	}
	if _, err := srv.run("new-session", "-d", "-s", name, "-n", "run", "-x", "240", "-y", "60",
		"-e", envTmuxSession+"="+name, "-e", envTmuxSocket+"="+srv.Socket, nav); err != nil {
		return err
	}
	watch, err := pane(append([]string{exe, "watch", id}, storeFlags...)...)
	if err != nil {
		return err
	}
	rn := shellQuote(append([]string{exe}, storeFlags...))
	shell, err := pane("sh", "-c", fmt.Sprintf(`printf 'A shell with the run'"'"'s variables opens here once its first step starts…\n'; %[1]s __waitrun %[2]s && clear && exec %[1]s shell %[2]s`, rn, id))
	if err != nil {
		return err
	}
	for _, c := range [][]string{
		{"set-option", "-t", name, "remain-on-exit", "on"},
		{"set-option", "-t", name, "status-left", "[runnr " + runbookName + "] "},
		{"set-option", "-t", name, "status-left-length", "60"},
		{"set-option", "-t", name, "status-right", "run " + id + "  (detach: prefix d · back: runnr attach)"},
		{"set-option", "-t", name, "status-right-length", "80"},
		{"set-option", "-t", name, "pane-border-status", "top"},
		{"split-window", "-h", "-d", "-t", name + ":run", "-l", "38%", "-e", envTmuxSession + "=" + name, "-e", envTmuxSocket + "=" + srv.Socket, watch},
		{"split-window", "-v", "-d", "-t", name + ":run.1", "-l", "55%", "-e", envTmuxSession + "=" + name, "-e", envTmuxSocket + "=" + srv.Socket, shell},
		{"select-pane", "-t", name + ":run.0", "-T", "run " + id},
		{"select-pane", "-t", name + ":run.1", "-T", "steps"},
		{"select-pane", "-t", name + ":run.2", "-T", "shell (the run's variables)"},
		{"select-pane", "-t", name + ":run.0"},
	} {
		if _, err := srv.run(c...); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "run %s is in tmux session %s (detach with prefix d; come back with: runnr attach %s)\n", id, name, id)
	return attachTmux(srv, name)
}

// attachTmux joins a session: switching to it inside tmux on the same
// server, else attaching (replacing this process).
func attachTmux(srv tmuxServer, name string) error {
	if os.Getenv("RUNNR_TMUX_NO_ATTACH") != "" { // tests
		return nil
	}
	if os.Getenv("TMUX") != "" && srv.Socket == "" {
		_, err := srv.run("switch-client", "-t", name)
		return err
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	argv := []string{"tmux"}
	if srv.Socket != "" {
		argv = append(argv, "-L", srv.Socket)
	}
	argv = append(argv, "attach-session", "-t", name)
	env := os.Environ()
	if srv.Socket != "" {
		env = withoutEnv(env, "TMUX") // a separate server: nesting is fine
	}
	return syscall.Exec(tmux, argv, env)
}

func withoutEnv(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// tmuxSessions lists runnr's sessions on both servers it uses.
func tmuxSessions() []struct {
	srv  tmuxServer
	name string
} {
	var out []struct {
		srv  tmuxServer
		name string
	}
	for _, srv := range []tmuxServer{{Socket: tmuxSocketName}, {}} {
		list, err := srv.run("list-sessions", "-F", "#{session_name}")
		if err != nil {
			continue
		}
		for _, n := range strings.Fields(list) {
			if strings.HasPrefix(n, "runnr-") {
				out = append(out, struct {
					srv  tmuxServer
					name string
				}{srv, n})
			}
		}
	}
	return out
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach [run-id|runbook]",
		Short: "Go back to a run started with --tmux",
		Long: `Attach to the tmux session of a run started with runnr run --tmux (or
resume --tmux). With no argument: the only such session, or a list.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needTmux(); err != nil {
				return err
			}
			all := tmuxSessions()
			var match []int
			for i, s := range all {
				if len(args) == 0 || strings.Contains(s.name, sessionUnsafe.ReplaceAllString(args[0], "-")) || (len(args[0]) >= 6 && strings.HasSuffix(s.name, args[0][len(args[0])-6:])) {
					match = append(match, i)
				}
			}
			switch len(match) {
			case 0:
				return errors.New("no runnr tmux sessions" + map[bool]string{true: " match " + strings.Join(args, ""), false: ""}[len(args) > 0])
			case 1:
				s := all[match[0]]
				return attachTmux(s.srv, s.name)
			}
			fmt.Println("Several runs are in tmux; attach to one:")
			for _, i := range match {
				fmt.Println("  runnr attach " + all[i].name)
			}
			return nil
		},
	}
}

// ---------- environment hand-off ----------

// writeEnvFile saves this process's environment for a tmux pane to start
// with (the tmux server's own may be stale), in a file only the user can
// read, which __tmuxenv deletes. (Not on the command line: ps shows that.)
func writeEnvFile() (string, error) {
	f, err := os.CreateTemp("", "runnr-env-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	env := withoutEnv(withoutEnv(os.Environ(), "TMUX"), "TMUX_PANE")
	if err := json.NewEncoder(f).Encode(env); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// tmuxEnvMain is runnr __tmuxenv FILE -- PROG ARGS: PROG with the
// environment in FILE (plus the tmux session's runnr variables).
func tmuxEnvMain(args []string) int {
	if len(args) < 3 || args[1] != "--" {
		fmt.Fprintln(os.Stderr, "usage: runnr __tmuxenv FILE -- PROG ARGS")
		return 2
	}
	b, err := os.ReadFile(args[0])
	os.Remove(args[0])
	var env []string
	if err == nil {
		err = json.Unmarshal(b, &env)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "runnr: the environment for this pane is gone:", err)
		return 1
	}
	for _, k := range []string{envTmuxSession, envTmuxSocket, "TMUX", "TMUX_PANE", "TERM"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(withoutEnv(env, k), k+"="+v)
		}
	}
	prog, err := exec.LookPath(args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "runnr:", err)
		return 1
	}
	if err := syscall.Exec(prog, args[2:], env); err != nil {
		fmt.Fprintln(os.Stderr, "runnr:", err)
	}
	return 1
}

// ---------- detached background steps ----------

// tmuxDetacher runs background steps in windows of the run's session.
type tmuxDetacher struct {
	srv     tmuxServer
	session string
	exe     string
}

func newTmuxDetacher() (*tmuxDetacher, error) {
	name := os.Getenv(envTmuxSession)
	if name == "" {
		return nil, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &tmuxDetacher{srv: tmuxServer{Socket: os.Getenv(envTmuxSocket)}, session: name, exe: exe}, nil
}

type tmuxRef struct {
	Server  tmuxServer `json:"server"`
	Session string     `json:"session"`
	Pane    string     `json:"pane"`
	Tmp     string     `json:"tmp"`
}

func (d *tmuxDetacher) Start(idx int, title string, spec engine.DetachedSpec) (engine.DetachedJob, error) {
	b, err := json.Marshal(struct {
		engine.DetachedSpec
		Title string `json:"title"`
	}{spec, title})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(spec.Tmp, "spec.json"), b, 0o600); err != nil {
		return nil, err
	}
	pane, err := d.srv.run("new-window", "-d", "-t", d.session+":", "-n", fmt.Sprintf("bg %d", idx+1), "-P", "-F", "#{pane_id}",
		shellQuote([]string{d.exe, "__detached", spec.Tmp}))
	if err != nil {
		return nil, err
	}
	_, _ = d.srv.run("set-option", "-w", "-t", pane, "remain-on-exit", "on")
	_, _ = d.srv.run("select-pane", "-t", pane, "-T", title)
	ref := tmuxRef{Server: d.srv, Session: d.session, Pane: pane, Tmp: spec.Tmp}
	return newTmuxJob(ref), nil
}

func (d *tmuxDetacher) Adopt(ref string) (engine.DetachedJob, error) {
	var r tmuxRef
	if err := json.Unmarshal([]byte(ref), &r); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(r.Tmp, "exit")); err == nil {
		return newTmuxJob(r), nil // finished on its own while no runnr followed it
	}
	pid, err := readPid(r.Tmp)
	if err != nil || syscall.Kill(pid, 0) != nil {
		return nil, fmt.Errorf("it isn't running any more and left no exit status")
	}
	return newTmuxJob(r), nil
}

type tmuxJob struct {
	ref  tmuxRef
	done chan struct{}
}

func newTmuxJob(r tmuxRef) *tmuxJob {
	j := &tmuxJob{ref: r, done: make(chan struct{})}
	go func() {
		defer close(j.done)
		for {
			if _, err := os.Stat(filepath.Join(r.Tmp, "exit")); err == nil {
				return
			}
			if pid, err := readPid(r.Tmp); err == nil && syscall.Kill(pid, 0) != nil {
				// Gone without an exit status (killed outright).
				time.Sleep(500 * time.Millisecond)
				if _, err := os.Stat(filepath.Join(r.Tmp, "exit")); err != nil {
					return
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	return j
}

func (j *tmuxJob) Ref() string {
	b, _ := json.Marshal(j.ref)
	return string(b)
}

func (j *tmuxJob) Done() <-chan struct{} { return j.done }

// Stop hangs up on the step (runnr __detached passes it on), then closes
// its window if it won't go.
func (j *tmuxJob) Stop() {
	if pid, err := readPid(j.ref.Tmp); err == nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	select {
	case <-j.done:
	case <-time.After(10 * time.Second):
		_, _ = j.ref.Server.run("kill-pane", "-t", j.ref.Pane)
		<-j.done
	}
}

func (j *tmuxJob) Wait() (*int, string, error) {
	<-j.done
	var code *int
	if b, err := os.ReadFile(filepath.Join(j.ref.Tmp, "exit")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			code = &n
		}
	}
	b, _ := os.ReadFile(filepath.Join(j.ref.Tmp, "log"))
	return code, engine.CleanTranscript(string(b)), nil
}

func readPid(tmp string) (int, error) {
	b, err := os.ReadFile(filepath.Join(tmp, "pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// detachedMain is runnr __detached TMP, in a background step's tmux
// window: it runs the step on a terminal, keeps its output in TMP/log and
// its exit status in TMP/exit (or "stopped" when hung up on).
func detachedMain(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: runnr __detached TMP")
		return 2
	}
	tmp := args[0]
	var spec struct {
		engine.DetachedSpec
		Title string `json:"title"`
	}
	b, err := os.ReadFile(filepath.Join(tmp, "spec.json"))
	if err == nil {
		err = json.Unmarshal(b, &spec)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "runnr:", err)
		return 1
	}
	os.Remove(filepath.Join(tmp, "spec.json")) // it holds the step's environment
	_ = os.WriteFile(filepath.Join(tmp, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	logf, err := os.OpenFile(filepath.Join(tmp, "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runnr:", err)
		return 1
	}
	defer logf.Close()
	fmt.Printf("\x1b[1mrunnr · background %s\x1b[0m\n\n", spec.Title)

	cmd := exec.Command(spec.Shell, "-c", spec.Command)
	cmd.Env, cmd.Dir = spec.Env, spec.Dir
	ptmx, err := pty.Start(cmd)
	if err != nil {
		writeExit(tmp, "1")
		fmt.Fprintln(os.Stderr, "runnr:", err)
		return 1
	}
	_ = pty.InheritSize(os.Stdin, ptmx)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	go func() {
		for range resize {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	// Hung up on (the run moved past stop_after, or ended): pass it on.
	stopped := make(chan struct{}, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		<-sig
		stopped <- struct{}{}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		time.Sleep(5 * time.Second)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}()
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			defer term.Restore(int(os.Stdin.Fd()), old)
		}
	}
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	_, _ = io.Copy(io.MultiWriter(os.Stdout, logf), ptmx)
	_ = cmd.Wait()
	code := strconv.Itoa(cmd.ProcessState.ExitCode())
	select {
	case <-stopped:
		code = "stopped"
	default:
	}
	writeExit(tmp, code)
	fmt.Printf("\r\n\x1b[2m── %s ──\x1b[0m\r\n", map[bool]string{true: "stopped", false: "exited " + code}[code == "stopped"])
	return 0
}

func writeExit(tmp, code string) {
	p := filepath.Join(tmp, "exit")
	_ = os.WriteFile(p+".tmp", []byte(code+"\n"), 0o600)
	_ = os.Rename(p+".tmp", p)
}

// waitRunCmd is runnr __waitrun ID: wait until the run's steps have started
// (its inputs are settled), or it has ended. Not "until a step isn't pending":
// a resume records the steps it carries over before it asks for its inputs,
// and the shell pane would then open without them, asking for every one.
func waitRunCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use: "__waitrun <run-id>", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for {
				if r, err := s().LoadRun(args[0]); err == nil && r.HasBegun() {
					return nil
				}
				time.Sleep(time.Second)
			}
		},
	}
}

// ---------- runnr watch ----------

func watchCmd(s func() *store.Store) *cobra.Command {
	var every time.Duration
	cmd := &cobra.Command{
		Use:   "watch <run-id>",
		Short: "Follow a run's steps as it goes (the tmux side pane)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := NewTerminal(0)
			for {
				r, err := s().LoadRun(args[0])
				var b strings.Builder
				if err != nil {
					fmt.Fprintf(&b, "Waiting for run %s to start…\n", args[0])
				} else {
					watchBoard(&b, t, r)
				}
				fmt.Print("\x1b[H\x1b[2J" + b.String())
				if err == nil && r.Status != engine.StatusRunning {
					return nil
				}
				time.Sleep(every)
			}
		},
	}
	cmd.Flags().DurationVar(&every, "every", 2*time.Second, "how often to look")
	return cmd
}

func watchBoard(b *strings.Builder, t *Terminal, r *engine.Run) {
	el := time.Since(r.Started)
	if !r.Ended.IsZero() {
		el = r.Ended.Sub(r.Started)
	}
	fmt.Fprintf(b, "%s  %s  %s\n", t.c("1", r.Runbook), t.c(statusStyle[r.Status].code, string(r.Status)), t.c("2", el.Round(time.Second).String()))
	if w := r.Window; w != nil && !w.Opened.IsZero() {
		if w.Closed.IsZero() {
			fmt.Fprintln(b, t.c("1;31", fmt.Sprintf("🔴 maintenance window open %s", time.Since(w.Opened).Round(time.Second))))
		} else {
			fmt.Fprintln(b, t.c("32", fmt.Sprintf("🟢 window closed after %s", w.Closed.Sub(w.Opened).Round(time.Second))))
		}
	}
	section := ""
	for i, st := range r.Steps {
		if st.Section != "" && st.Section != section {
			section = st.Section
			fmt.Fprintln(b, t.c("2;1", "  "+section))
		}
		line := fmt.Sprintf("%s %2d %s", statusIcon[st.Status], i+1, st.Name)
		switch st.Status {
		case engine.StatusRunning:
			line = t.c("1", line)
		case engine.StatusBackground:
			line += t.c("2", " (background)")
		case engine.StatusPending:
			line = t.c("2", line)
		}
		if st.Status == engine.StatusFailed && st.Error != "" {
			line += "\n      " + t.c("31", firstLineOf(st.Error))
		}
		fmt.Fprintln(b, "  "+line)
	}
	if r.Error != "" {
		fmt.Fprintln(b, "\n"+t.c("31", r.Error))
	}
}

func firstLineOf(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// shellQuote joins argv for sh.
func shellQuote(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`).MatchString(a) {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}
