// Package cli implements the runnr command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/server"
	"github.com/egrif/runnr/internal/store"
)

// Set via -ldflags "-X github.com/egrif/runnr/internal/cli.version=..."
var version = "dev"

// ExitError carries a process exit code out of a command.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

func Execute() int {
	err := newRoot().Execute()
	var ee ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	var home, dsn string
	var st *store.Store
	root := &cobra.Command{
		Use:           "runnr",
		Short:         "Create, manage and run runbooks — interactively or unattended",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			st, err = store.Open(home, dsn)
			return err
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			if st != nil {
				return st.Close()
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&home, "home", store.DefaultHome(), "runnr home; holds runnr.db unless --db is set (env RUNNR_HOME)")
	root.PersistentFlags().StringVar(&dsn, "db", os.Getenv("RUNNR_DATABASE_URL"), "postgres:// URL or SQLite file (env RUNNR_DATABASE_URL; default <home>/runnr.db)")
	s := func() *store.Store { return st }

	root.AddCommand(
		listCmd(s), showCmd(s), newCmd(s), editCmd(s), importCmd(s), deleteCmd(s),
		validateCmd(s), versionsCmd(s), libCmd(s), collectionCmd(s), inspectCmd(s), runCmd(s), rerunCmd(s), resumeCmd(s), shellCmd(s), noteCmd(s), historyCmd(s), serveCmd(s), dbCmd(s),
	)
	return root
}

func listCmd(s func() *store.Store) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List runbooks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := s().List()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No runbooks yet. Create one with: runnr new <name>")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVER\tSTEPS\tDESCRIPTION")
			for _, e := range entries {
				desc := e.Description
				if e.Err != "" {
					desc = "INVALID: " + firstLine(e.Err)
				}
				fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", e.Name, e.Version, e.Steps, firstLine(desc))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func showCmd(s func() *store.Store) *cobra.Command {
	var ver int
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Print a runbook's YAML (current, or --version N)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var b []byte
			var err error
			if ver > 0 {
				b, err = s().RawVersion(args[0], ver)
			} else {
				b, err = s().Raw(args[0])
			}
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(b)
			return err
		},
	}
	cmd.Flags().IntVar(&ver, "version", 0, "print this saved version")
	return cmd
}

func versionsCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "versions <name>",
		Short: "List a runbook's saved versions",
		Long:  "Every save is kept. Print one with `runnr show <name> --version N`; restore it with\n`runnr show <name> --version N | runnr import -f -`.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vs, err := s().Versions(args[0])
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "VERSION\tSAVED\tBYTES")
			for _, v := range vs {
				fmt.Fprintf(w, "%d\t%s\t%d\n", v.Version, v.CreatedAt.Local().Format("2006-01-02 15:04:05"), v.Bytes)
			}
			return w.Flush()
		},
	}
}

func newCmd(s func() *store.Store) *cobra.Command {
	var noEdit bool
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a runbook from a starter template and open it in $EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if s().Exists(name) {
				return fmt.Errorf("runbook %q already exists", name)
			}
			raw := []byte(runbook.Scaffold(name))
			if _, _, err := s().Save(raw, store.SaveOpts{}); err != nil {
				return err
			}
			fmt.Println("created", name)
			if noEdit || !isTTY(os.Stdin) {
				return nil
			}
			return editLoop(s(), name)
		},
	}
	cmd.Flags().BoolVar(&noEdit, "no-edit", false, "don't open an editor")
	return cmd
}

func editCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a runbook in $EDITOR (validated on save)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editLoop(s(), args[0])
		},
	}
}

// editLoop edits a temp copy and only replaces the stored runbook once valid.
func editLoop(st *store.Store, name string) error {
	doc, err := st.Load(name)
	if err != nil {
		return err
	}
	raw := doc.Raw
	tmp, err := os.CreateTemp("", "runnr-"+name+"-*.yaml")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	tmp.Close()

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	t := NewTerminal(0)
	for {
		c := exec.Command("sh", "-c", editor+` "$1"`, "sh", tmp.Name())
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("editor: %w", err)
		}
		edited, err := os.ReadFile(tmp.Name())
		if err != nil {
			return err
		}
		rb, ver, err := st.Save(edited, store.SaveOpts{OldName: name, BaseVersion: doc.Version})
		if err == nil {
			fmt.Printf("saved %s (version %d)\n", rb.Name, ver)
			return nil
		}
		if errors.Is(err, store.ErrConflict) {
			keep = true
			return fmt.Errorf("%w\nyour edits are in %s; merge them into `runnr edit %s` again", err, tmp.Name(), name)
		}
		fmt.Fprintln(os.Stderr, "invalid runbook:\n"+indent(err.Error()))
		// Read directly (not via the shared keyboard reader), since the
		// editor needs stdin to itself next.
		fmt.Fprint(os.Stderr, t.c("33;1", "  ? ")+"[e]dit again / [d]iscard changes: ")
		var a string
		_, _ = fmt.Fscanln(os.Stdin, &a)
		if strings.HasPrefix(strings.ToLower(a), "d") {
			return errors.New("changes discarded")
		}
	}
}

func importCmd(s func() *store.Store) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "import <file>...",
		Short: "Validate and add runbook files to the library (- reads stdin)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var failed bool
			for _, f := range args {
				var raw []byte
				var err error
				if f == "-" {
					raw, err = io.ReadAll(os.Stdin)
				} else {
					raw, err = os.ReadFile(f)
				}
				if err == nil {
					var rb *runbook.Runbook
					var ver int
					if rb, ver, err = s().Save(raw, store.SaveOpts{Overwrite: force}); err == nil {
						fmt.Printf("imported %s → %s (version %d)\n", f, rb.Name, ver)
						continue
					}
				}
				failed = true
				fmt.Fprintf(os.Stderr, "%s:\n%s\n", f, indent(err.Error()))
			}
			if failed {
				return ExitError{1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing runbooks")
	return cmd
}

func deleteCmd(s func() *store.Store) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a runbook",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !s().Exists(args[0]) {
				return fmt.Errorf("runbook %q: %w", args[0], store.ErrNotFound)
			}
			if !force {
				if !isTTY(os.Stdin) {
					return errors.New("refusing to delete without --force when not interactive")
				}
				a, err := NewTerminal(0).choose(context.Background(), fmt.Sprintf("Delete %s? [y/N]", args[0]), map[string]string{"y": "y", "n": "n"}, "n")
				if err != nil || a != "y" {
					return err
				}
			}
			if err := s().Delete(args[0]); err != nil {
				return err
			}
			fmt.Println("deleted", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "don't ask for confirmation")
	return cmd
}

func validateCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "validate [name|file]...",
		Short: "Validate runbooks (all in the library if none given)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				entries, err := s().List()
				if err != nil {
					return err
				}
				for _, e := range entries {
					args = append(args, e.Name)
				}
			}
			var failed bool
			for _, ref := range args {
				if _, _, err := s().Resolve(ref); err != nil {
					failed = true
					fmt.Printf("✘ %s\n%s\n", ref, indent(err.Error()))
				} else {
					fmt.Printf("✔ %s\n", ref)
				}
			}
			if failed {
				return ExitError{1}
			}
			return nil
		},
	}
}

type runFlags struct {
	vars                     []string
	unattended, yes, dry, js bool
	quiet, noHistory         bool
	stepMode, noReview       bool
	skip, before, after      string
}

func (f *runFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringArrayVarP(&f.vars, "var", "v", nil, "set an input (key=value), repeatable")
	fl.BoolVarP(&f.unattended, "unattended", "u", false, "never prompt")
	fl.BoolVarP(&f.yes, "yes", "y", false, "approve confirm-gated steps when unattended")
	fl.BoolVar(&f.dry, "dry-run", false, "render steps without executing them")
	fl.BoolVar(&f.js, "json", false, "unattended; print the run record as JSON instead of progress")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "hide command output")
	fl.BoolVar(&f.noHistory, "no-history", false, "don't record this run")
	fl.BoolVar(&f.stepMode, "step", false, "pause before every step (run / skip / view / shell / …)")
	fl.BoolVar(&f.noReview, "no-review", false, "start without reviewing the plan")
	fl.StringVar(&f.skip, "skip", "", "steps to leave out, e.g. 3,5-7")
	fl.StringVar(&f.before, "pause-before", "", "steps to stop before (run / skip / view / shell), e.g. 4,9")
	fl.StringVar(&f.after, "pause-after", "", "steps to stop after, to check the result before going on")
}

// execute runs rb with these flags; base holds inputs that --var overrides.
// launch describes what to run beyond the runbook and flags.
type launch struct {
	base     map[string]string // inputs that --var overrides
	startAt  int
	resumes  string
	state    map[string]string
	previous *engine.Run // shown as "last time"
	resumed  *engine.Run // the run this continues
}

func (f *runFlags) execute(st *store.Store, rb *runbook.Runbook, ver int, l launch) error {
	cv := engine.ControlsView{StepAll: f.stepMode}
	for flag, dst := range map[string]*[]int{f.skip: &cv.Skip, f.before: &cv.PauseBefore, f.after: &cv.PauseAfter} {
		if flag == "" {
			continue
		}
		idxs, err := parseRanges(flag, len(rb.Steps))
		if err != nil {
			return fmt.Errorf("step list %q: %w", flag, err)
		}
		*dst = idxs
	}
	if l.previous == nil {
		// Compare with the latest run of this runbook, if there is one.
		if runs, err := st.ListRuns(rb.Name, 1); err == nil && len(runs) == 1 {
			l.previous, _ = st.LoadRun(runs[0].ID)
		}
	}
	base, startAt, resumes, state := l.base, l.startAt, l.resumes, l.state
	values := map[string]string{}
	for k, v := range base {
		values[k] = v
	}
	for _, kv := range f.vars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--var %q must be key=value", kv)
		}
		values[k] = v
	}
	interactive := !f.unattended && !f.js && isTTY(os.Stdin)

	term := NewTerminal(len(rb.Steps))
	term.quiet = f.quiet
	term.sections = map[string]string{}
	for _, sec := range rb.Sections {
		term.sections[sec.Name] = sec.Description
	}
	var ui engine.Interactor = term
	if !interactive {
		onEvent := term.Notify
		if f.js {
			onEvent = nil
		}
		ui = engine.Unattended{OnEvent: onEvent, AssumeYes: f.yes}
	}

	ctx, stop, paused := interruptible()
	defer stop()
	term.interrupts = paused
	if !f.noHistory {
		term.addNote = func(step int, text string) error {
			_, err := st.AddNote(term.runID, step, author(), text)
			return err
		}
	}
	libs, err := st.Libraries(rb.Libraries)
	if err != nil {
		return err
	}
	opts := engine.Options{
		Vars: values, DryRun: f.dry, Interactive: interactive, StartAt: startAt,
		Controls: engine.NewControls(cv), Review: interactive && !f.noReview, Previous: l.previous,
		State: state, Resumes: resumes, RunbookVersion: ver, Libraries: libs,
		// Only a recorded run of a saved runbook can run dangerous steps
		// without a double check.
		Sanctioned: ver > 0 && !f.noHistory && !f.dry, Resumed: l.resumed,
	}
	if !f.noHistory {
		opts.Save = func(r *engine.Run) {
			if err := st.SaveRun(r); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving run history:", err)
			}
		}
	}
	run := engine.Execute(ctx, rb, ui, "", opts)
	if f.js {
		_ = printJSON(run)
	} else if run.Status != engine.StatusSucceeded && !f.noHistory && engine.ResumePoint(run) >= 0 {
		fmt.Fprintf(os.Stderr, "resume with: runnr resume %s\n", run.ID)
	}
	switch run.Status {
	case engine.StatusSucceeded:
		return nil
	case engine.StatusAborted:
		return ExitError{130}
	}
	return ExitError{1}
}

// interruptible returns a context cancelled by SIGINT/SIGTERM. While paused
// is set (an interactive shell is open), Ctrl-C belongs to the shell.
func interruptible() (context.Context, func(), *atomic.Bool) {
	ctx, cancel := context.WithCancel(context.Background())
	paused := &atomic.Bool{}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range ch {
			if sig == os.Interrupt && paused.Load() {
				continue
			}
			cancel()
		}
	}()
	return ctx, func() { signal.Stop(ch); cancel() }, paused
}

func author() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "cli"
}

func runCmd(s func() *store.Store) *cobra.Command {
	var f runFlags
	var from string
	cmd := &cobra.Command{
		Use:   "run <name|file>",
		Short: "Run a runbook",
		Long: `Run a runbook by library name or file path.

Interactive (default on a terminal): prompts for missing inputs, manual
steps, confirmations, and what to do when a step fails. Steps marked tty
take over the terminal.

Unattended (--unattended, or when stdin isn't a terminal): no prompts.
Inputs come from --var or defaults, confirm-gated steps need --yes, manual
and tty steps follow their 'unattended' policy, and any failure aborts.

--from starts at a later step (by id, number or name); steps marked
'always' (e.g. environment setup) still run.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rb, ver, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			start := 0
			if from != "" {
				if start, err = rb.StepIndex(from); err != nil {
					return err
				}
			}
			return f.execute(s(), rb, ver, launch{startAt: start})
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&from, "from", "", "start at this step (id, number or name)")
	return cmd
}

func rerunCmd(s func() *store.Store) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "rerun <run-id>",
		Short: "Run a runbook again with an earlier run's inputs, comparing against it",
		Long: `Starts a new run of the same runbook with the same inputs (secret inputs are
asked again). The review shows what each step did in that earlier run.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prev, err := s().LoadRun(args[0])
			if err != nil {
				return err
			}
			rb, ver, err := s().Get(prev.Runbook)
			if err != nil {
				return err
			}
			return f.execute(s(), rb, ver, launch{base: engine.ResumeInputs(prev), previous: prev})
		},
	}
	f.register(cmd)
	return cmd
}

func resumeCmd(s func() *store.Store) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Continue a failed or aborted run from where it stopped",
		Long: `Start a new run of the same runbook with the same inputs, beginning at the
first step that didn't succeed. Secret inputs aren't stored, so they're asked
for again (or pass them with -v). Steps marked 'always' run again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prev, err := s().LoadRun(args[0])
			if err != nil {
				return err
			}
			start := engine.ResumePoint(prev)
			if start < 0 {
				return fmt.Errorf("run %s has no unfinished steps", prev.ID)
			}
			rb, ver, err := s().Get(prev.Runbook)
			if err != nil {
				return err
			}
			if len(rb.Steps) != len(prev.Steps) || rb.Steps[start].Name != prev.Steps[start].Name {
				return fmt.Errorf("runbook %q changed since run %s; use: runnr run %s --from <step>", rb.Name, prev.ID, rb.Name)
			}
			fmt.Printf("resuming %s at step %d (%s)\n", prev.ID, start+1, rb.Steps[start].Name)
			return f.execute(s(), rb, ver, launch{base: engine.ResumeInputs(prev), startAt: start, resumes: prev.ID, state: engine.ResumeState(prev), previous: prev, resumed: prev})
		},
	}
	f.register(cmd)
	return cmd
}

func inspectCmd(s func() *store.Store) *cobra.Command {
	var vars []string
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:   "inspect <runbook|file> [step]",
		Short: "Show what a runbook will do: every step, or one step in full",
		Long: `Without a step, lists every step with its kind and flags. With a step (id,
number or exact name), shows its code, expected outcome, directory, and the
values it publishes. --all shows every step in full.

Templates are filled in from inputs (defaults, or -v); values earlier steps
would publish show as <name>.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rb, _, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			values := map[string]string{}
			for _, kv := range vars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--var %q must be key=value", kv)
				}
				values[k] = v
			}
			plan := engine.Preview(rb, values, nil)
			if len(args) == 2 {
				i, err := rb.StepIndex(args[1])
				if err != nil {
					return err
				}
				plan = plan[i : i+1]
				all = true
			}
			if asJSON {
				return printJSON(plan)
			}
			t := NewTerminal(len(rb.Steps))
			if all {
				for _, v := range plan {
					fmt.Println(t.viewText(v, "") + "\n")
				}
				return nil
			}
			fmt.Println(t.c("1", rb.Name))
			if rb.Description != "" {
				fmt.Println(indent(strings.TrimSpace(rb.Description)))
			}
			if rb.Expect != "" {
				fmt.Println(t.c("32", "  By the end: ") + strings.ReplaceAll(strings.TrimSpace(rb.Expect), "\n", "\n              "))
			}
			sections := map[string]runbook.Section{}
			for _, sec := range rb.Sections {
				sections[sec.Name] = sec
			}
			section := ""
			for _, v := range plan {
				if v.Section != "" && v.Section != section {
					section = v.Section
					fmt.Println(t.c("35;1", "══ "+section))
					if d := sections[section].Description; d != "" {
						fmt.Println(t.c("2", "   "+firstLine(d)))
					}
				}
				flags := []string{v.Kind}
				if v.Always {
					flags = append(flags, "always")
				}
				if v.Confirm {
					flags = append(flags, "confirm")
				}
				if v.Dangerous {
					flags = append(flags, "dangerous")
				}
				if v.When != "" {
					flags = append(flags, "conditional")
				}
				if v.Owner != "" {
					flags = append(flags, "owner: "+v.Owner)
				}
				fmt.Printf("%3d  %s %s\n", v.Index+1, v.Name, t.c("2", strings.Join(flags, " · ")))
			}
			fmt.Println(t.c("2", "\nDetails: runnr inspect "+rb.Name+" <step>   (or --all)"))
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&vars, "var", "v", nil, "fill in an input (key=value)")
	cmd.Flags().BoolVar(&all, "all", false, "show every step in full")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func noteCmd(s func() *store.Store) *cobra.Command {
	var step string
	cmd := &cobra.Command{
		Use:   "note <run-id> <text>",
		Short: "Add a note to a run, or to one of its steps with --step",
		Long:  "Notes can be added while the run is in progress (from another terminal) or afterwards.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := s().LoadRun(args[0])
			if err != nil {
				return err
			}
			idx := -1
			if step != "" {
				if idx, err = runStepIndex(r, step); err != nil {
					return err
				}
			}
			if _, err := s().AddNote(r.ID, idx, author(), args[1]); err != nil {
				return err
			}
			where := "run"
			if idx >= 0 {
				where = fmt.Sprintf("step %d (%s)", idx+1, r.Steps[idx].Name)
			}
			fmt.Printf("noted on %s\n", where)
			return nil
		},
	}
	cmd.Flags().StringVarP(&step, "step", "s", "", "step number or exact name")
	return cmd
}

func runStepIndex(r *engine.Run, ref string) (int, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(r.Steps) {
			return 0, fmt.Errorf("step %d out of range 1-%d", n, len(r.Steps))
		}
		return n - 1, nil
	}
	for i, st := range r.Steps {
		if st.Name == ref {
			return i, nil
		}
	}
	return 0, fmt.Errorf("run %s has no step %q", r.ID, ref)
}

func shellCmd(s func() *store.Store) *cobra.Command {
	var vars []string
	cmd := &cobra.Command{
		Use:   "shell <runbook|file|run-id>",
		Short: "Run a runbook's setup steps, then open a shell with that environment",
		Long: `Runs only the steps marked 'always' (the setup) and opens your $SHELL with
everything they published, plus inputs and the runbook's env, in the
environment. Nothing is recorded in history.

Given a run ID, the shell uses that run's runbook and inputs (secret inputs
are asked for again).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTTY(os.Stdin) {
				return errors.New("runnr shell needs a terminal")
			}
			base := map[string]string{}
			rb, _, err := s().Resolve(args[0])
			if err != nil {
				prev, rerr := s().LoadRun(args[0])
				if rerr != nil {
					return err
				}
				if rb, _, err = s().Get(prev.Runbook); err != nil {
					return err
				}
				base = engine.ResumeInputs(prev)
			}
			for _, kv := range vars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--var %q must be key=value", kv)
				}
				base[k] = v
			}
			ctx, stop, paused := interruptible()
			defer stop()
			term := NewTerminal(len(rb.Steps))
			term.interrupts = paused
			libs, err := s().Libraries(rb.Libraries)
			if err != nil {
				return err
			}
			run := engine.Execute(ctx, rb, term, "", engine.Options{Vars: base, Interactive: true, ShellOnly: true, Libraries: libs})
			if run.Status != engine.StatusSucceeded {
				return ExitError{1}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&vars, "var", "v", nil, "set an input (key=value), repeatable")
	return cmd
}

func historyCmd(s func() *store.Store) *cobra.Command {
	var (
		filter string
		limit  int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "history [run-id]",
		Short: "List past runs, or show one in detail",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				r, err := s().LoadRun(args[0])
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(r)
				}
				printRun(r)
				return nil
			}
			runs, err := s().ListRuns(filter, limit)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(runs)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "RUN ID\tRUNBOOK\tVER\tSTATUS\tMODE\tSTARTED\tDURATION")
			for _, r := range runs {
				dur := "-"
				if !r.Ended.IsZero() {
					dur = r.Ended.Sub(r.Started).Round(time.Millisecond).String()
				}
				ver := "-"
				if r.RunbookVersion > 0 {
					ver = fmt.Sprint(r.RunbookVersion)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Runbook, ver, r.Status, r.Mode, r.Started.Local().Format("2006-01-02 15:04:05"), dur)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&filter, "runbook", "r", "", "only runs of this runbook")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "max runs to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.AddCommand(pruneCmd(s))
	return cmd
}

func pruneCmd(s func() *store.Store) *cobra.Command {
	var olderThan string
	cmd := &cobra.Command{
		Use:   "prune --older-than <age>",
		Short: "Delete finished runs older than an age like 90d or 720h",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			age, err := parseAge(olderThan)
			if err != nil {
				return err
			}
			n, err := s().PruneRuns(time.Now().Add(-age))
			if err != nil {
				return err
			}
			fmt.Printf("deleted %d run(s) older than %s\n", n, olderThan)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "age, e.g. 90d, 2w or 720h (required)")
	_ = cmd.MarkFlagRequired("older-than")
	return cmd
}

// parseAge accepts Go durations plus d (days) and w (weeks).
func parseAge(s string) (time.Duration, error) {
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			v, err := strconv.Atoi(n)
			if err != nil || v <= 0 {
				return 0, fmt.Errorf("invalid age %q", s)
			}
			return time.Duration(v) * unit, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid age %q (try 90d, 2w or 720h)", s)
	}
	return d, nil
}

func dbCmd(s func() *store.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Inspect the database, or import file-based data into it",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show where data lives and how much there is",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := s().Stats()
			if err != nil {
				return err
			}
			fmt.Printf("driver:    %s\nlocation:  %s\nrunbooks:  %d (%d saved versions)\nruns:      %d\n", st.Driver, st.Location, st.Runbooks, st.Versions, st.Runs)
			return nil
		},
	}, &cobra.Command{
		Use:   "import <dir>",
		Short: "Import <dir>/runbooks/*.yaml and <dir>/runs/*.json (e.g. into a shared Postgres)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rbs, runs, err := s().ImportFiles(args[0])
			fmt.Printf("imported %d runbook(s) and %d run(s) into %s\n", rbs, runs, s().Location)
			return err
		},
	})
	return cmd
}

func printRun(r *engine.Run) {
	fmt.Printf("Run %s — %s (%s)\n", r.ID, r.Runbook, r.Mode)
	if r.RunbookVersion > 0 {
		fmt.Printf("Version: %d\n", r.RunbookVersion)
	}
	fmt.Printf("Status:  %s\n", r.Status)
	if r.Error != "" {
		fmt.Printf("Error:   %s\n", r.Error)
	}
	fmt.Printf("Started: %s\n", r.Started.Format(time.RFC3339))
	for k, v := range r.Inputs {
		fmt.Printf("Input:   %s=%s\n", k, v)
	}
	printNotes(r.Notes, "")
	if len(r.State) > 0 {
		fmt.Println("State:")
		keys := make([]string, 0, len(r.State))
		for k := range r.State {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, r.State[k])
		}
	}
	for _, st := range r.Steps {
		fmt.Printf("\n[%d] %s — %s", st.Index+1, st.Name, st.Status)
		if st.Note != "" {
			fmt.Printf(" (%s)", st.Note)
		}
		fmt.Println()
		if st.Command != "" {
			fmt.Println(indent("$ " + st.Command))
		}
		if st.Output != "" {
			fmt.Println(indent(strings.TrimRight(st.Output, "\n")))
		}
		if st.Error != "" {
			fmt.Println(indent("error: " + st.Error))
		}
		for _, c := range st.Checks {
			mark := "✅"
			if !c.Passed {
				mark = "❌"
			}
			fmt.Printf("    %s %s\n", mark, c.Name)
		}
		if st.Alternative != "" {
			fmt.Printf("    done by: %s\n", st.Alternative)
			for k, sub := range st.Sub {
				fmt.Printf("      %d. %s — %s %s\n", k+1, sub.Name, sub.Status, sub.Note)
			}
		}
		printNotes(st.Notes, "    ")
	}
}

func serveCmd(s func() *store.Store) *cobra.Command {
	var addr, token string
	var open, noUI bool
	cmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"ui"},
		Short:   "Serve the web GUI and HTTP API",
		Long: `Serve the web GUI and the HTTP API (see docs/API.md).

Without a token, runnr only listens on loopback. To listen on other
interfaces, set --token (or RUNNR_TOKEN); API clients then send
"Authorization: Bearer <token>" and the GUI asks for it once.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" && !isLoopback(addr) {
				return fmt.Errorf("refusing to listen on %s without --token: anyone who can reach it could run commands", addr)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			srv := server.New(s(), server.Config{Token: token, NoUI: noUI})
			url := "http://" + addr
			what := "GUI and API"
			if noUI {
				what = "API"
			}
			auth := "no auth (loopback only)"
			if token != "" {
				auth = "token required"
			}
			fmt.Printf("runnr %s on %s (%s)\n", what, url, auth)
			if open && !noUI {
				go openBrowser(url)
			}
			return srv.ListenAndServe(ctx, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7777", "listen address")
	cmd.Flags().StringVar(&token, "token", os.Getenv("RUNNR_TOKEN"), "API token (env RUNNR_TOKEN)")
	cmd.Flags().BoolVar(&open, "open", false, "open the GUI in the default browser")
	cmd.Flags().BoolVar(&noUI, "no-ui", false, "serve only the API")
	return cmd
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	for _, c := range []string{"open", "xdg-open"} {
		if p, err := exec.LookPath(c); err == nil {
			_ = exec.Command(p, url).Start()
			return
		}
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func printNotes(notes []engine.Note, prefix string) {
	for _, n := range notes {
		fmt.Printf("%s✎ %s %s: %s\n", prefix, n.Time.Local().Format("2006-01-02 15:04"), n.Author, n.Text)
	}
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func libCmd(s func() *store.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lib",
		Short: "Manage function libraries runbooks can use",
		Long: `A library is a named set of shell functions. A runbook that lists it under
libraries: can call its functions in every step, and they're loaded into
shells opened from runnr. To use them in any other terminal:

  eval "$(runnr lib export <name>)"`,
	}
	var noEdit, force bool
	newC := &cobra.Command{
		Use: "new <name>", Short: "Create a library from a starter and open it in $EDITOR", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := s().SaveLibrary([]byte(runbook.LibraryScaffold(args[0])), store.SaveOpts{}); err != nil {
				return err
			}
			fmt.Println("created library", args[0])
			if noEdit || !isTTY(os.Stdin) {
				return nil
			}
			return editLibrary(s(), args[0])
		},
	}
	newC.Flags().BoolVar(&noEdit, "no-edit", false, "don't open an editor")
	importC := &cobra.Command{
		Use: "import <file>...", Short: "Add library files (- reads stdin)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, f := range args {
				var raw []byte
				var err error
				if f == "-" {
					raw, err = io.ReadAll(os.Stdin)
				} else {
					raw, err = os.ReadFile(f)
				}
				if err != nil {
					return err
				}
				lib, ver, err := s().SaveLibrary(raw, store.SaveOpts{Overwrite: force})
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Printf("imported %s → library %s (version %d)\n", f, lib.Name, ver)
			}
			return nil
		},
	}
	importC.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing libraries")
	var ver int
	showC := &cobra.Command{
		Use: "show <name>", Short: "Print a library's YAML (--version N for an older save)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ver > 0 {
				b, err := s().RawLibraryVersion(args[0], ver)
				if err == nil {
					_, err = os.Stdout.Write(b)
				}
				return err
			}
			d, err := s().LoadLibrary(args[0])
			if err == nil {
				_, err = os.Stdout.Write(d.Raw)
			}
			return err
		},
	}
	showC.Flags().IntVar(&ver, "version", 0, "print this saved version")
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List libraries and their functions", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				libs, err := s().ListLibraries()
				if err != nil {
					return err
				}
				if len(libs) == 0 {
					fmt.Println("No libraries yet. Create one with: runnr lib new <name>")
					return nil
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tVER\tFUNCTIONS\tDESCRIPTION")
				for _, l := range libs {
					sort.Strings(l.Functions)
					desc := l.Description
					if l.Err != "" {
						desc = "INVALID: " + firstLine(l.Err)
					}
					fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", l.Name, l.Version, strings.Join(l.Functions, ", "), desc)
				}
				return w.Flush()
			},
		},
		showC, newC, importC,
		&cobra.Command{
			Use: "edit <name>", Short: "Edit a library in $EDITOR (validated on save)", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error { return editLibrary(s(), args[0]) },
		},
		&cobra.Command{
			Use: "delete <name>", Aliases: []string{"rm"}, Short: "Delete a library (its versions are kept)", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := s().DeleteLibrary(args[0]); err != nil {
					return err
				}
				fmt.Println("deleted library", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use: "versions <name>", Short: "List a library's saved versions", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				vs, err := s().LibraryVersions(args[0])
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "VERSION\tSAVED\tBYTES")
				for _, v := range vs {
					fmt.Fprintf(w, "%d\t%s\t%d\n", v.Version, v.CreatedAt.Local().Format("2006-01-02 15:04:05"), v.Bytes)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "export <name>...",
			Short: "Print libraries as shell function definitions",
			Long:  "Use them in any shell: eval \"$(runnr lib export <name>)\"",
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				libs, err := s().Libraries(args)
				if err != nil {
					return err
				}
				for _, l := range libs {
					fmt.Printf("# runnr library %s\n%s", l.Name, runbook.FunctionScript(l.Functions))
				}
				return nil
			},
		},
	)
	return cmd
}

// editDoc edits a temp copy of a stored YAML document and only saves it
// once valid.
func editDoc(noun, name string, raw []byte, save func([]byte) (string, int, error)) error {
	tmp, err := os.CreateTemp("", "runnr-"+noun+"-"+name+"-*.yaml")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	tmp.Close()
	editor := nonEmptyEnv("VISUAL", nonEmptyEnv("EDITOR", "vi"))
	for {
		c := exec.Command("sh", "-c", editor+` "$1"`, "sh", tmp.Name())
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("editor: %w", err)
		}
		edited, err := os.ReadFile(tmp.Name())
		if err != nil {
			return err
		}
		saved, ver, err := save(edited)
		if err == nil {
			fmt.Printf("saved %s %s (version %d)\n", noun, saved, ver)
			return nil
		}
		if errors.Is(err, store.ErrConflict) {
			keep = true
			return fmt.Errorf("%w\nyour edits are in %s", err, tmp.Name())
		}
		fmt.Fprintf(os.Stderr, "invalid %s:\n%s\n", noun, indent(err.Error()))
		fmt.Fprint(os.Stderr, "  ? [e]dit again / [d]iscard changes: ")
		var a string
		_, _ = fmt.Fscanln(os.Stdin, &a)
		if strings.HasPrefix(strings.ToLower(a), "d") {
			return errors.New("changes discarded")
		}
	}
}

func editLibrary(st *store.Store, name string) error {
	doc, err := st.LoadLibrary(name)
	if err != nil {
		return err
	}
	return editDoc("library", name, doc.Raw, func(raw []byte) (string, int, error) {
		lib, ver, err := st.SaveLibrary(raw, store.SaveOpts{OldName: name, BaseVersion: doc.Version})
		if err != nil {
			return "", 0, err
		}
		return lib.Name, ver, nil
	})
}

func collectionCmd(s func() *store.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "collection",
		Aliases: []string{"collections"},
		Short:   "Group runbooks that belong together, with what they're for",
	}
	var noEdit, force bool
	newC := &cobra.Command{
		Use: "new <name>", Short: "Create a collection and open it in $EDITOR", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := s().SaveCollection([]byte(runbook.CollectionScaffold(args[0])), store.SaveOpts{}); err != nil {
				return err
			}
			fmt.Println("created collection", args[0])
			if noEdit || !isTTY(os.Stdin) {
				return nil
			}
			d, err := s().LoadCollection(args[0])
			if err != nil {
				return err
			}
			return editDoc("collection", args[0], []byte(d.Raw), func(raw []byte) (string, int, error) {
				return s().SaveCollection(raw, store.SaveOpts{OldName: args[0], BaseVersion: d.Version})
			})
		},
	}
	newC.Flags().BoolVar(&noEdit, "no-edit", false, "don't open an editor")
	importC := &cobra.Command{
		Use: "import <file>...", Short: "Add collection files (- reads stdin)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, f := range args {
				var raw []byte
				var err error
				if f == "-" {
					raw, err = io.ReadAll(os.Stdin)
				} else {
					raw, err = os.ReadFile(f)
				}
				if err != nil {
					return err
				}
				name, ver, err := s().SaveCollection(raw, store.SaveOpts{Overwrite: force})
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Printf("imported %s → collection %s (version %d)\n", f, name, ver)
			}
			return nil
		},
	}
	importC.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing collections")
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List collections and their runbooks", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cs, err := s().ListCollections()
				if err != nil {
					return err
				}
				if len(cs) == 0 {
					fmt.Println("No collections yet. Create one with: runnr collection new <name>")
					return nil
				}
				for _, c := range cs {
					if c.Collection == nil {
						fmt.Printf("%s  INVALID: %s\n", c.Name, firstLine(c.Err))
						continue
					}
					fmt.Printf("%s — %s\n", c.Name, firstLine(c.Collection.Description))
					for n, r := range c.Collection.Runbooks {
						fmt.Printf("  %d. %s\n", n+1, r)
					}
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "show <name>", Short: "Print a collection's YAML", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				d, err := s().LoadCollection(args[0])
				if err == nil {
					fmt.Print(d.Raw)
				}
				return err
			},
		},
		newC, importC,
		&cobra.Command{
			Use: "edit <name>", Short: "Edit a collection in $EDITOR", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				d, err := s().LoadCollection(args[0])
				if err != nil {
					return err
				}
				return editDoc("collection", args[0], []byte(d.Raw), func(raw []byte) (string, int, error) {
					return s().SaveCollection(raw, store.SaveOpts{OldName: args[0], BaseVersion: d.Version})
				})
			},
		},
		&cobra.Command{
			Use: "delete <name>", Aliases: []string{"rm"}, Short: "Delete a collection (its runbooks stay)", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := s().DeleteCollection(args[0]); err != nil {
					return err
				}
				fmt.Println("deleted collection", args[0])
				return nil
			},
		},
	)
	return cmd
}

func nonEmptyEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
