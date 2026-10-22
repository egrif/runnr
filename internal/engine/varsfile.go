package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The run's values for its shells, kept up to date: once an interactive
// shell opens (the operator's, a pane's, the console's), the run writes its
// variables, secrets included, to a file only its user can read, and
// rewrites it whenever they change. runnr's zsh and bash shells read it
// again when it has changed ($RUNNR_VARS): zsh before each prompt and each
// command, bash before each prompt. So a shell opened early sees what later
// steps publish. The file goes when the run
// ends.

type varsFile struct {
	off  bool // a shell outside a run (OpenShell): its values don't change
	mu   sync.Mutex
	dir  string
	path string
	ver  int
}

// varsPath is the file, made (and written) the first time it's asked for.
func (x *executor) varsPath() string {
	if x.run == nil || x.run.Begun.IsZero() || x.vf.off {
		return "" // a shell outside a run has its values already
	}
	x.vf.mu.Lock()
	if x.vf.path == "" {
		dir, err := x.tempDir("runnr-vars-")
		if err != nil {
			x.vf.mu.Unlock()
			return ""
		}
		x.vf.dir, x.vf.path = dir, filepath.Join(dir, "vars.sh")
	}
	p := x.vf.path
	x.vf.mu.Unlock()
	x.writeVars()
	return p
}

// writeVars rewrites the file, if there is one, with the run's values.
func (x *executor) writeVars() {
	x.vf.mu.Lock()
	defer x.vf.mu.Unlock()
	if x.vf.path == "" {
		return
	}
	x.vmu.RLock()
	names := make([]string, 0, len(x.vars))
	for k := range x.vars {
		if identRe.MatchString(k) {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	x.vf.ver++
	var b strings.Builder
	fmt.Fprintf(&b, "RUNNR_VARS_VER=%d\n", x.vf.ver)
	for _, k := range names {
		fmt.Fprintf(&b, "export %s=%s\n", k, shQuote(x.vars[k]))
	}
	x.vmu.RUnlock()
	tmp := x.vf.path + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) == nil {
		_ = os.Rename(tmp, x.vf.path)
	}
}

func (x *executor) removeVars() {
	x.vf.mu.Lock()
	defer x.vf.mu.Unlock()
	if x.vf.dir != "" {
		os.RemoveAll(x.vf.dir)
		x.vf.dir, x.vf.path = "", ""
	}
}

// varsHook reads $RUNNR_VARS again when it has changed (before a prompt).
const varsHook = `runnr_vars_refresh() {
  [ -n "$RUNNR_VARS" ] && [ -f "$RUNNR_VARS" ] || return 0
  local v; IFS= read -r v < "$RUNNR_VARS" 2>/dev/null
  [ "$v" = "$RUNNR_VARS_SEEN" ] && return 0
  . "$RUNNR_VARS"; RUNNR_VARS_SEEN=$v
}
`
