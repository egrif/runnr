package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/egrif/runnr/internal/runbook"
)

// FuncInfo describes a function steps and shells can call.
type FuncInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	From        string `json:"from"` // library name, or "runbook"
}

// setupFunctions writes the run's functions (libraries in order, then the
// runbook's own, which win) to a file every step sources.
func (x *executor) setupFunctions() error {
	all := map[string]FuncInfo{}
	var script string
	add := func(from string, fns map[string]runbook.Function) {
		script += runbook.FunctionScript(fns)
		for n, f := range fns {
			all[n] = FuncInfo{Name: n, Description: f.Description, From: from}
		}
	}
	for _, l := range x.opts.Libraries {
		add(l.Name, l.Functions)
	}
	add("runbook", x.rb.Functions)
	// Every step can ask the operator a question (runnr_ask, runnr_confirm).
	script += askFunctions
	all["runnr_ask"] = FuncInfo{Name: "runnr_ask", Description: "Ask the operator a question and print the answer. Usage: runnr_ask PROMPT", From: "runnr"}
	all["runnr_confirm"] = FuncInfo{Name: "runnr_confirm", Description: "Ask the operator yes or no; succeeds on yes. Usage: runnr_confirm PROMPT", From: "runnr"}
	f, err := os.CreateTemp(x.opts.TempDir, "runnr-functions-*.sh")
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(script); err != nil {
		return err
	}
	x.funcFile = f.Name()
	for _, fi := range all {
		x.functions = append(x.functions, fi)
	}
	sort.Slice(x.functions, func(i, j int) bool { return x.functions[i].Name < x.functions[j].Name })
	return nil
}

func (x *executor) Functions() []FuncInfo { return x.functions }

// interactiveShell is the runbook's shell (else the operator's $SHELL),
// interactive, with their config for it loaded and then the run's
// functions. done cleans up afterwards.
func (x *executor) interactiveShell() (cmd *exec.Cmd, env []string, done func(), err error) {
	sh := interactiveShellName(x.rb.Shell)
	done = func() {}
	if x.funcFile == "" {
		return x.command(context.Background(), sh), nil, done, nil
	}
	dir, err := x.tempDir("runnr-rc-")
	if err != nil {
		return nil, nil, done, err
	}
	done = func() { os.RemoveAll(dir) }
	home, _ := os.UserHomeDir()
	switch filepath.Base(sh) {
	case "zsh":
		// zsh reads its startup files from $ZDOTDIR: chain to the real ones,
		// then load the functions.
		orig := os.Getenv("ZDOTDIR")
		if orig == "" {
			orig = home
		}
		files := map[string]string{
			".zshenv": `[ -f "$RUNNR_ZDOTDIR/.zshenv" ] && . "$RUNNR_ZDOTDIR/.zshenv"` + "\n",
			".zshrc":  `ZDOTDIR="$RUNNR_ZDOTDIR"; [ -f "$ZDOTDIR/.zshrc" ] && . "$ZDOTDIR/.zshrc"; . "$RUNNR_FUNCTIONS"` + "\n",
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				return nil, nil, done, err
			}
		}
		return x.command(context.Background(), sh), []string{"ZDOTDIR=" + dir, "RUNNR_ZDOTDIR=" + orig}, done, nil
	case "bash":
		rc := filepath.Join(dir, "bashrc")
		body := `[ -f "$HOME/.bashrc" ] && . "$HOME/.bashrc"; . "$RUNNR_FUNCTIONS"` + "\n"
		if err := os.WriteFile(rc, []byte(body), 0o600); err != nil {
			return nil, nil, done, err
		}
		return x.command(context.Background(), sh, "--rcfile", rc, "-i"), nil, done, nil
	}
	// Other shells: the functions are in $RUNNR_FUNCTIONS to source by hand.
	return x.command(context.Background(), sh), nil, done, nil
}
