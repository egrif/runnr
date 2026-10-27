// Package cli implements the runnr command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/egrif/runnr/internal/claude"
	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/htmlview"
	"github.com/egrif/runnr/internal/project"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/runtest"
	"github.com/egrif/runnr/internal/server"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/timeline"
	"github.com/egrif/runnr/internal/ver"
)

// Set via -ldflags "-X github.com/egrif/runnr/internal/cli.version=..."
// (the release build and make); see Version.
var version = "dev"

// ExitError carries a process exit code out of a command.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

func Execute() int {
	// A faked command in a test calls back here: answer it, quickly, without
	// opening the store.
	if len(os.Args) > 2 && os.Args[1] == "__fake" {
		return runtest.Dispatch(os.Args[2], os.Args[3:])
	}
	if len(os.Args) == 3 && os.Args[1] == "__probe" {
		return runtest.Probe(os.Args[2])
	}
	if len(os.Args) > 1 && os.Args[1] == "__detached" {
		return detachedMain(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "__tmuxenv" {
		return tmuxEnvMain(os.Args[2:])
	}
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
		Version:       Version(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			storeFlags = []string{"--home", home}
			if dsn != "" {
				storeFlags = append(storeFlags, "--db", dsn)
			}
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
		listCmd(s), showCmd(s), newCmd(s), editCmd(s), importCmd(s), deleteCmd(s), restoreCmd(s),
		validateCmd(s), versionsCmd(s), libCmd(s), collectionCmd(s), inspectCmd(s), runCmd(s), rerunCmd(s), resumeCmd(s), abortCmd(s), suspendCmd(s), testCmd(s), testsCmd(s), fakesCmd(s), shellCmd(s), noteCmd(s), saveEditsCmd(s), scheduleCmd(s), exportCmd(s), explainCmd(s), historyCmd(s), serveCmd(s), dbCmd(s), versionCmd(),
		attachCmd(), watchCmd(s), waitRunCmd(s), doctorCmd(s),
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
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", e.Name, e.Version.Short(), e.Steps, firstLine(desc))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func showCmd(s func() *store.Store) *cobra.Command {
	var rev string
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Print a runbook's YAML (current, or --version V)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var b []byte
			var err error
			if rev != "" {
				b, _, err = s().RawVersion(args[0], rev)
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
	cmd.Flags().StringVar(&rev, "version", "", "print this saved version (its hash, a prefix of it, or its short form)")
	return cmd
}

func versionsCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "versions <name>",
		Short: "List a runbook's saved versions",
		Long:  "Every save is kept. Print one with `runnr show <name> --version V` (its hash, a prefix of it, or the\nshort form listed); make it current again with `runnr show <name> --version V | runnr import -f -`.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vs, err := s().Versions(args[0])
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "VERSION\tFIRST SAVED\tBYTES\t")
			for _, v := range vs {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", v.Version.Short(), v.CreatedAt.Local().Format("2006-01-02 15:04:05"), v.Bytes, map[bool]string{true: "current"}[v.Current])
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
		rb, rev, err := st.Save(edited, store.SaveOpts{OldName: name, Base: doc.Version})
		if err == nil {
			fmt.Printf("saved %s (version %s)\n", rb.Name, rev.Short())
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
	var force, dry, noFound, showDiff bool
	var collection, as string
	var sets, renames []string
	cmd := &cobra.Command{
		Use:   "import <file|folder>...",
		Short: "Add runbooks, libraries, fakes and test suites: files, or a project folder (- reads stdin)",
		Long: `Add runnr files to the store: runbooks, function libraries, fake libraries
and test suites. Give files, or a folder (its .yaml files, in subfolders
too; other YAML is left alone). - reads a runbook from stdin.

Placeholders, @NAME@, are filled in: @PROJECT_DIR@ is the folder (else the
current directory), @REPO_ROOT@ its git root, others --set NAME=VALUE.

Each file's references are checked: a runbook's libraries, a suite's
runbook and fake libraries must be in the import, stored, or built in. A
library that's missing but defined once elsewhere in the repository is
imported too (--no-found leaves it out).

A file whose name is taken is refused, unless --force (it becomes the
current version of that one; earlier versions are kept) or --rename
NAME=NEW (imported as NEW; references to it in the other files follow).
--as NEW renames a single file. --diff shows what saving over each taken
name would change (and imports nothing). Files are saved libraries first, then
fakes, runbooks and suites; one that can't go in (an error, a placeholder
without a value, a missing reference) is reported and left out.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := project.Options{Values: map[string]string{}, Renames: map[string]string{}, Lookup: project.StoreLookup(s())}
			for _, a := range args {
				if st, err := os.Stat(a); err == nil && st.IsDir() {
					if o.Dir != "" {
						return errors.New("give one folder (and any files)")
					}
					o.Dir = a
				} else {
					o.Paths = append(o.Paths, a)
				}
			}
			for _, kv := range sets {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--set %s: want NAME=VALUE", kv)
				}
				o.Values[strings.Trim(k, "@")] = v
			}
			if len(args) == 1 && args[0] == "-" {
				if showDiff {
					return errors.New("--diff takes files or a folder, not stdin")
				}
				return importStdin(s(), force, collection, o.Values)
			}
			rn := map[string]string{}
			for _, kv := range renames {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--rename %s: want NAME=NEW", kv)
				}
				rn[k] = v
			}
			p, err := project.Scan(o)
			if err != nil {
				return err
			}
			if as != "" {
				if len(p.Docs) != 1 {
					return errors.New("--as renames one file: give one (or use --rename NAME=NEW)")
				}
				rn[p.Docs[0].Original] = as
			}
			// Renames by name, whatever the kind; libraries found elsewhere in the repository.
			again := len(rn) > 0
			for _, d := range p.Docs {
				if v, ok := rn[d.Original]; ok {
					o.Renames[d.Kind+":"+d.Original] = v
				}
				for _, r := range d.Refs {
					if r.Found == "" && len(r.Candidates) == 1 && !noFound && !slices.Contains(o.Include, r.Candidates[0]) {
						o.Include = append(o.Include, r.Candidates[0])
						again = true
					}
				}
			}
			if again {
				if p, err = project.Scan(o); err != nil {
					return err
				}
			}
			for _, d := range p.Docs {
				if d.Included {
					fmt.Printf("also importing %s (%s %s, needed here)\n", d.Rel, d.Kind, d.Name)
				}
			}
			if dry || showDiff {
				return printPlan(p, showDiff)
			}
			res := project.Apply(p, func(project.Doc) bool { return force }, project.StoreSaver(s()))
			var failed bool
			var imported []string
			for _, r := range res {
				switch r.Status {
				case "failed", "exists":
					failed = true
					fmt.Fprintln(os.Stderr, r)
				default:
					fmt.Println(r)
				}
				if r.Kind == project.Runbook && r.Status != "failed" && r.Status != "skipped" {
					imported = append(imported, r.Name)
				}
			}
			if len(p.Unset) > 0 {
				fmt.Fprintf(os.Stderr, "placeholders without a value (--set NAME=VALUE): %s\n", strings.Join(p.Unset, ", "))
			}
			if collection != "" && len(imported) > 0 {
				_, created, err := s().AddToCollection(collection, true, imported...)
				if err != nil {
					return fmt.Errorf("collection %s: %w", collection, err)
				}
				fmt.Printf("added to collection %s%s: %s\n", collection, map[bool]string{true: " (new)"}[created], strings.Join(imported, ", "))
			}
			if failed {
				return ExitError{1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "save over files with the same name (as their current version)")
	cmd.Flags().StringVar(&as, "as", "", "import a single file under this name (its name: is changed)")
	cmd.Flags().StringArrayVar(&renames, "rename", nil, "import NAME as NEW (NAME=NEW; references to it follow)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "a placeholder's value (NAME=VALUE, for @NAME@)")
	cmd.Flags().BoolVar(&noFound, "no-found", false, "don't import missing libraries found elsewhere in the repository")
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "say what would be imported, and change nothing")
	cmd.Flags().BoolVar(&showDiff, "diff", false, "as --dry-run, with what saving over each taken name would change (a diff)")
	cmd.Flags().StringVarP(&collection, "collection", "c", "", "add the imported runbooks to this collection")
	return cmd
}

// printPlan says what an import would do; with diffs, what saving over
// each taken name would change.
func printPlan(p *project.Plan, diffs bool) error {
	fmt.Printf("folder %s", p.Dir)
	if p.RepoRoot != "" {
		fmt.Printf(" (repository %s)", p.RepoRoot)
	}
	fmt.Println()
	for _, d := range p.Docs {
		state := "new"
		switch {
		case d.Error != "":
			state = "error: " + d.Error
		case d.Same:
			state = fmt.Sprintf("unchanged (version %s)", d.Version.Short())
		case d.Exists:
			state = fmt.Sprintf("name taken (version %s)", d.Version.Short())
		}
		if d.Diff != "" {
			add, rm := project.DiffStat(d.Diff)
			state += fmt.Sprintf(": -f saves it as version %s (+%d −%d lines)", ver.Of(d.Raw).Short(), add, rm)
		}
		fmt.Printf("  %-8s %-28s %s  %s\n", d.Kind, d.Name, d.Rel, state)
		if diffs && d.Diff != "" {
			printDiff(d.Diff, "      ")
		}
		for _, r := range d.Refs {
			if r.Found == "" {
				fmt.Printf("           needs %s %s%s\n", r.Kind, r.Name, map[bool]string{true: " (found: " + strings.Join(r.Candidates, ", ") + ")"}[len(r.Candidates) > 0])
			}
		}
	}
	if len(p.Unset) > 0 {
		fmt.Printf("placeholders without a value: %s\n", strings.Join(p.Unset, ", "))
	}
	return p.OK()
}

// printDiff prints a unified diff, indented, in colour on a terminal.
func printDiff(d, indent string) {
	color := isTTY(os.Stdout) && os.Getenv("NO_COLOR") == ""
	for _, l := range strings.Split(strings.TrimSuffix(d, "\n"), "\n") {
		c := ""
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			c = "1"
		case strings.HasPrefix(l, "@@"):
			c = "36"
		case strings.HasPrefix(l, "+"):
			c = "32"
		case strings.HasPrefix(l, "-"):
			c = "31"
		}
		if color && c != "" {
			l = "\x1b[" + c + "m" + l + "\x1b[0m"
		}
		fmt.Println(indent + l)
	}
}

// importStdin imports one runbook from standard input.
func importStdin(st *store.Store, force bool, collection string, values map[string]string) error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	raw, _, unset := project.Fill(raw, values)
	if len(unset) > 0 {
		return fmt.Errorf("placeholders without a value (--set NAME=VALUE): %s", strings.Join(unset, ", "))
	}
	var prev ver.ID
	if rb, err := runbook.Parse(raw); err == nil {
		if d, err := st.Load(rb.Name); err == nil {
			prev = d.Version
		}
	}
	rb, rev, err := st.Save(raw, store.SaveOpts{Overwrite: force})
	if err != nil {
		if !force && prev != "" {
			err = fmt.Errorf("%w (version %s): --force makes this its current version", err, prev.Short())
		}
		return err
	}
	fmt.Println(importReport("-", rb.Name, rev, prev))
	if collection != "" {
		if _, _, err := st.AddToCollection(collection, true, rb.Name); err != nil {
			return err
		}
	}
	return nil
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
			also, err := s().DeleteRunbook(args[0])
			if err != nil {
				return err
			}
			fmt.Println("deleted", args[0], "(its versions are kept: runnr restore runbook "+args[0]+")")
			for _, a := range also {
				fmt.Println("  " + a)
			}
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
	vars                                   []string
	unattended, yes, dry, js, usePreferred bool
	quiet, noHistory                       bool
	stepMode, noReview                     bool
	skip, before, after                    string
	tmux                                   bool
	runID                                  string
	newRun                                 bool // resume: a new run, not this one continued
}

func (f *runFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringArrayVarP(&f.vars, "var", "v", nil, "set an input (key=value), repeatable")
	fl.BoolVarP(&f.unattended, "unattended", "u", false, "never prompt")
	fl.BoolVarP(&f.yes, "yes", "y", false, "approve confirm-gated steps when unattended")
	fl.BoolVar(&f.usePreferred, "use-preferred", false, "unattended: steps set to ask which way use their preferred way (asked otherwise)")
	fl.BoolVar(&f.dry, "dry-run", false, "render steps without executing them")
	fl.BoolVar(&f.js, "json", false, "unattended; print the run record as JSON instead of progress")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "hide command output")
	fl.BoolVar(&f.noHistory, "no-history", false, "don't record this run")
	fl.BoolVar(&f.stepMode, "step", false, "pause before every step (run / skip / view / shell / …)")
	fl.BoolVar(&f.noReview, "no-review", false, "start without reviewing the plan")
	fl.StringVar(&f.skip, "skip", "", "steps to leave out, e.g. 3,5-7")
	fl.StringVar(&f.before, "pause-before", "", "steps to stop before (run / skip / view / shell), e.g. 4,9")
	fl.StringVar(&f.after, "pause-after", "", "steps to stop after, to check the result before going on")
	fl.BoolVar(&f.tmux, "tmux", false, "run in a tmux session: it outlives this terminal (runnr attach), with the steps and a shell beside it, and background steps in windows of their own that outlive runnr")
	fl.StringVar(&f.runID, "run-id", "", "the new run's ID")
	_ = fl.MarkHidden("run-id")
}

// offerSaveEdits reminds the operator that steps were changed during the
// run, and offers to save them to the runbook.
func offerSaveEdits(st *store.Store, run *engine.Run, t *Terminal, ask bool) {
	n := len(engine.LatestEdits(run.Edits))
	fmt.Fprintf(os.Stderr, "\n✎ %d step(s) were changed during this run; the runbook doesn't have the change yet.\n", n)
	if !st.HasVersion(run.Runbook, run.RunbookVersion) {
		fmt.Fprintln(os.Stderr, "  This runbook ran from a file: copy the change into it by hand.")
		return
	}
	if ask {
		fmt.Fprintf(os.Stderr, "  Save to %s as a new version? [y/N]: ", run.Runbook)
		if a, err := t.readLine(context.Background()); err == nil && strings.HasPrefix(strings.ToLower(a), "y") {
			if rev, err := st.SaveRunEdits(run); err != nil {
				fmt.Fprintln(os.Stderr, "  couldn't save:", err)
			} else {
				fmt.Fprintf(os.Stderr, "  saved %s (version %s)\n", run.Runbook, rev.Short())
				return
			}
		}
	}
	fmt.Fprintf(os.Stderr, "  Save it later with: runnr save-edits %s\n", run.ID)
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

func (f *runFlags) execute(st *store.Store, rb *runbook.Runbook, rev ver.ID, l launch) error {
	if f.tmux && os.Getenv(envTmuxSession) == "" {
		if f.js || f.noHistory {
			return errors.New("--tmux can't be used with --json or --no-history")
		}
		id := engine.NewRunID()
		if l.resumed != nil && !f.newRun {
			id = l.resumed.ID // the run carries on
		}
		return startTmuxRun(os.Args[1:], rb.Name, id)
	}
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
		// Compare with the latest run of this runbook, if there is one (not
		// the run being carried on).
		if runs, err := st.ListRuns(rb.Name, 2); err == nil {
			for _, r := range runs {
				if l.resumed == nil || r.ID != l.resumed.ID {
					l.previous, _ = st.LoadRun(r.ID)
					break
				}
			}
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
	if !interactive && !f.usePreferred && !f.dry {
		if ch := rb.ChoosingSteps(cv.Skip); len(ch) > 0 {
			var names []string
			for _, i := range ch {
				names = append(names, fmt.Sprintf("%d (%s)", i+1, rb.Steps[i].Name))
			}
			msg := fmt.Sprintf("steps %s normally ask which way to do them; unattended, they'll use their preferred way", strings.Join(names, ", "))
			if !isTTY(os.Stdin) {
				return fmt.Errorf("%s. Add --use-preferred to go ahead", msg)
			}
			fmt.Fprintf(os.Stderr, "%s. Go ahead? [y/N]: ", msg)
			if a, err := NewTerminal(0).readLine(context.Background()); err != nil || !strings.HasPrefix(strings.ToLower(a), "y") {
				return errors.New("not started")
			}
		}
	}

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

	ictx, stop, paused := interruptible()
	defer stop()
	term.interrupts = paused
	// Another runnr (runnr abort, the GUI) can ask for this run to stop.
	ctx, abort := context.WithCancelCause(ictx)
	defer abort(nil)
	id := engine.NewRunID()
	if l.resumed != nil && !f.newRun {
		id = l.resumed.ID // the run carries on, in its own record
	}
	if f.runID != "" {
		id = f.runID
	}
	ctl := engine.NewControls(cv)
	term.ctl = ctl
	if !f.noHistory && !f.dry {
		go st.WatchAbort(ctx, id, abort, ctl)
	}
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
		Owner: engine.ThisProcess("cli"), Vars: values, DryRun: f.dry, Interactive: interactive, StartAt: startAt,
		Controls: ctl, Review: interactive && !f.noReview, Previous: l.previous,
		State: state, Resumes: resumes, RunbookVersion: rev, LibraryVersions: st.LibrariesUsed(rb.Libraries), Libraries: libs,
		// Only a recorded run of a saved runbook can run dangerous steps
		// without a double check.
		Sanctioned: st.HasVersion(rb.Name, rev) && !f.noHistory && !f.dry, Resumed: l.resumed, Continue: l.resumed != nil && !f.newRun,
		// A resume leaves out every step that finished, not only those
		// before where it starts.
		// (Unless the operator chose the steps with --skip.)
		CarryDone: l.resumed != nil && f.skip == "",
	}
	if f.tmux {
		d, err := newTmuxDetacher()
		if err != nil {
			return err
		}
		if d != nil {
			opts.Detached = d
		}
	}
	if !f.noHistory {
		opts.Save = func(r *engine.Run) {
			if err := st.SaveRun(r); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving run history:", err)
			}
		}
	}
	run := engine.Execute(ctx, rb, ui, id, opts)
	if len(run.Edits) > 0 && !f.js {
		offerSaveEdits(st, run, term, interactive && !f.noHistory)
	}
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
	case engine.StatusSuspended:
		return ExitError{75} // EX_TEMPFAIL: to be done later
	}
	return ExitError{1}
}

// interruptible returns a context cancelled by SIGINT/SIGTERM. While
// something holds Ctrl-C (an interactive shell is open, a tty step runs or
// has just failed), Ctrl-C belongs to it.
func interruptible() (context.Context, func(), *interruptHolds) {
	ctx, cancel := context.WithCancel(context.Background())
	holds := &interruptHolds{out: os.Stdout}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range ch {
			if held, _ := holds.held(); sig == os.Interrupt && held {
				holds.ignored()
				continue
			}
			if sig == os.Interrupt && holds.stepFirst() {
				continue
			}
			cancel()
		}
	}()
	return ctx, func() { signal.Stop(ch); cancel() }, holds
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
			rb, rev, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			start := 0
			if from != "" {
				if start, err = rb.StepIndex(from); err != nil {
					return err
				}
			}
			return f.execute(s(), rb, rev, launch{startAt: start})
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
			rb, rev, err := s().Get(prev.Runbook)
			if err != nil {
				return err
			}
			return f.execute(s(), rb, rev, launch{base: engine.ResumeInputs(prev), previous: prev})
		},
	}
	f.register(cmd)
	return cmd
}

func resumeCmd(s func() *store.Store) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Continue a failed, aborted or suspended run from where it stopped",
		Long: `Carry on the run from the first step that didn't succeed, with the same
inputs, in the same run: its record and log go on (runnr history <id>),
with the steps that finished as they went, and the earlier tries of the
steps it does again kept beside them. --new-run starts a new run instead,
which points back at this one. Steps that finished in that run, before or
after it, are left out (the review can put one back; with --skip, only the
steps you list are). An optional background step that didn't finish (a
watcher) runs again. Secret inputs aren't
stored, so they're asked for again (or pass them with -v). Steps marked
'always' run again.`,
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
			rb, rev, err := s().Get(prev.Runbook)
			if err != nil {
				return err
			}
			if engine.RunbookChanged(prev, rb) {
				if f.newRun {
					return fmt.Errorf("runbook %q changed since run %s; use: runnr run %s --from <step> (or resume without --new-run to carry the run on with this version)", rb.Name, prev.ID, rb.Name)
				}
				// The run carries on with this version: its steps matched by name.
				aligned, moved := engine.AlignRun(prev, rb)
				names := map[int]string{}
				for i, st := range prev.Steps {
					names[i] = st.Name
				}
				if err := s().MoveRunNotes(prev.ID, moved, names); err != nil {
					return err
				}
				gone := len(aligned.Removed) - len(prev.Removed)
				fmt.Printf("runbook %s changed since this run (version %s, it ran version %s): carrying on with it, steps matched by name%s\n",
					rb.Name, rev.Short(), prev.RunbookVersion.Short(), map[bool]string{true: fmt.Sprintf("; %d step(s) no longer in it are kept in the record", gone), false: ""}[gone > 0])
				prev = aligned
				if start = engine.ResumePoint(prev); start < 0 {
					return fmt.Errorf("run %s has no unfinished steps in this version", prev.ID)
				}
			}
			fmt.Printf("resuming %s at step %d (%s)\n", prev.ID, start+1, rb.Steps[start].Name)
			l := launch{base: engine.ResumeInputs(prev), startAt: start, resumes: prev.ID, state: engine.ResumeState(prev), previous: prev, resumed: prev}
			if !f.newRun {
				// The same run: compared with what it was compared with before.
				l.resumes, l.previous = "", nil
				if prev.Previous != "" {
					l.previous, _ = s().LoadRun(prev.Previous)
				}
			}
			return f.execute(s(), rb, rev, l)
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&f.newRun, "new-run", false, "start a new run (pointing back at this one) instead of carrying this one on")
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
			if mw := rb.MaintenanceWindow; mw != nil {
				line := "  Maintenance window (production down)"
				if mw.Target != "" {
					line += ": target " + mw.Target
				}
				if lead, ok := mw.NotifyLead(); ok {
					line += ", notification due " + runbook.FormatSpan(lead) + " before"
				}
				if o, c := rb.WindowSteps(); o >= 0 {
					line += fmt.Sprintf(", steps %d", o+1)
					if c >= 0 {
						line += fmt.Sprintf("–%d", c+1)
					} else {
						line += "– (no step closes it)"
					}
				}
				fmt.Println(t.c("31", line))
			}
			if o := strings.TrimSpace(rb.Overview); o != "" {
				fmt.Println(t.c("1", "  Overview"))
				fmt.Println(indent(o))
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
				if len(v.Ways) > 1 {
					var names []string
					for _, w := range v.Ways {
						n := w.Name
						if w.Preferred {
							n += "*"
						}
						names = append(names, n)
					}
					flags[0] = "ways: " + strings.Join(names, ", ")
					if v.Choose {
						flags = append(flags, "asks which")
					}
				}
				if v.Window != "" {
					flags = append(flags, t.c("31", "window "+v.Window+"s"))
				}
				if v.Offset != "" {
					flags = append(flags, "T"+v.Offset)
				}
				if v.IgnoredOffset != "" {
					flags = append(flags, t.c("33", "T"+v.IgnoredOffset+" out of order, ignored"))
				}
				if v.Always {
					flags = append(flags, "always")
				}
				if v.Optional {
					flags = append(flags, t.c("33", "optional"))
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

func explainCmd(s func() *store.Store) *cobra.Command {
	var ask string
	var fresh bool
	cmd := &cobra.Command{
		Use:   "explain <runbook|file> [step]",
		Short: "Ask a model (Claude by default) about a step, or the whole runbook",
		Long: `Asks Claude what a step does, what it relies on from earlier steps (the
setup steps especially), what to check, its risks and how to improve it;
-q asks your own question instead. The whole runbook (credentials masked)
and its library functions go along as context, cached for a few minutes,
so asking about other steps of it soon after costs much less.

Answers are kept: the same question about an unchanged runbook is answered
from runnr's store, free (--fresh asks again).

Set up a model with environment variables:
  ANTHROPIC_API_KEY            Claude through Anthropic (default ` + claude.DefaultModel + `)
  RUNNR_AI_PROVIDER=bedrock    Claude through Amazon Bedrock: RUNNR_AI_MODEL is
                               the model or inference profile id, with
                               RUNNR_AI_AWS_PROFILE (or AWS_PROFILE) and AWS_REGION
  RUNNR_AI_PROVIDER=openai     any OpenAI-compatible API: RUNNR_AI_BASE_URL
                               (default OpenAI; e.g. http://localhost:11434/v1
                               for Ollama), RUNNR_AI_API_KEY or OPENAI_API_KEY
  RUNNR_AI_MODEL               the model (RUNNR_CLAUDE_MODEL also works)
  RUNNR_AI_CACHE_TTL=1h        Anthropic: keep the runbook in the prompt cache
                               for an hour instead of five minutes`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := claude.FromEnv()
			if c == nil {
				return errors.New("no model is set up: set ANTHROPIC_API_KEY, or see runnr explain --help for Bedrock and others")
			}
			rb, _, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			var raw []byte
			if d, err := s().Load(args[0]); err == nil {
				raw = d.Raw
			} else if b, err := os.ReadFile(args[0]); err == nil {
				raw = b
			}
			libs, err := s().Libraries(rb.Libraries)
			if err != nil {
				return err
			}
			step := 0
			if len(args) == 2 {
				i, err := rb.StepIndex(args[1])
				if err != nil {
					return err
				}
				step = i + 1
			}
			u, err := claude.Explain(cmd.Context(), c, s(), rb, raw, libs, step, ask, nil, fresh, func(t string) { fmt.Print(t) })
			fmt.Println()
			if err != nil {
				return err
			}
			if u.Local {
				fmt.Fprintf(os.Stderr, "\n%s %s · answered from runnr's store, free (--fresh to ask again)\n", c.Name(), c.ModelName())
			} else {
				fmt.Fprintf(os.Stderr, "\n%s %s · %d tokens in (%d from the prompt cache, %d written to it), %d out\n", c.Name(), c.ModelName(), u.Input+u.CacheRead+u.CacheWrite, u.CacheRead, u.CacheWrite, u.Output)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ask, "question", "q", "", "your own question about the step (or runbook)")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "ask again instead of using a stored answer")
	return cmd
}

func exportCmd(s func() *store.Store) *cobra.Command {
	var vars, libs []string
	var out, format string
	var noLibs, force bool
	cmd := &cobra.Command{
		Use:   "export <runbook|file>",
		Short: "Write a runbook as Markdown to follow by hand, an HTML page to read, or YAML files (with its libraries)",
		Long: `Writes the runbook as a Markdown document: a collapsed overview, a setup
block (inputs, environment, helper functions), then each section and step
with what it's doing and its code, in collapsible blocks.

Paste the code blocks, in order, into one shell (the runbook's shell).
Placeholders become shell variables; steps that exit, change directory or
set shell options run in a subshell; values steps publish still land in
your shell. --var fills in inputs.

--format html (or -o file.html) writes one self-contained page instead:
sections and steps that expand and collapse, ways as tabs, and each step's
code as written or exactly as runnr would run it (templates filled in, the
files it sources, how it captures published values).

--format yaml (or -o file.yaml) writes the runbook as written, with the
libraries it uses beside it as <name>.yaml (runnr's built-in ones left
out), so the folder imports again: --lib NAME chooses which (repeat),
--no-libs leaves them out, -f writes over files already there.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format == "yaml" || (format == "" && (strings.HasSuffix(out, ".yaml") || strings.HasSuffix(out, ".yml"))) {
				return exportYAML(s(), args[0], out, libs, noLibs, force)
			}
			rb, _, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			libs, err := s().Libraries(rb.Libraries)
			if err != nil {
				return err
			}
			vals := map[string]string{}
			for _, kv := range vars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--var %q must be key=value", kv)
				}
				vals[k] = v
			}
			if format == "" {
				format = "md"
				if strings.HasSuffix(strings.ToLower(out), ".html") {
					format = "html"
				}
			}
			var doc []byte
			switch format {
			case "md", "markdown":
				md, err := runbook.ExportMarkdown(rb, runbook.ExportOptions{Vars: vals, Libraries: libs})
				if err != nil {
					return err
				}
				doc = []byte(md)
			case "html":
				if doc, err = htmlview.Render(rb, htmlview.Options{Vars: vals, Libraries: libs}); err != nil {
					return err
				}
			default:
				return fmt.Errorf("--format must be md or html")
			}
			if out == "" || out == "-" {
				os.Stdout.Write(doc)
				return nil
			}
			if err := os.WriteFile(out, doc, 0o644); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "wrote", out)
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&vars, "var", "v", nil, "fill in an input (key=value)")
	cmd.Flags().StringVarP(&out, "output", "o", "", "write to this file (default: standard output)")
	cmd.Flags().StringVarP(&format, "format", "f", "", "md, html for a page to read (sections and steps that expand, code as written or as runnr runs it), or yaml: the runbook and its libraries as files (default: from -o's extension, else md)")
	cmd.Flags().StringArrayVar(&libs, "lib", nil, "yaml: a library to write beside it (repeat; default: all it uses but runnr's own)")
	cmd.Flags().BoolVar(&noLibs, "no-libs", false, "yaml: leave the libraries out")
	cmd.Flags().BoolVar(&force, "force", false, "yaml: write over files already there")
	return cmd
}

func abortCmd(s func() *store.Store) *cobra.Command {
	var reason string
	var force bool
	cmd := &cobra.Command{
		Use:   "abort <run-id>",
		Short: "Abort a run that's running elsewhere (another terminal, the GUI), or whose process is gone",
		Long: `Asks the runnr running a run (a runnr run in another terminal, or runnr
serve) to abort it, as if its operator had: the step running now is
stopped and the run ends aborted, ready to resume. In the terminal running
it, Ctrl-C or [a]bort at a prompt does the same.

When the process that ran it is gone (it exited on this machine), the run
is marked aborted straight away. --force marks it aborted without waiting,
for a run left "running" by a process on another machine that's gone.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := s().AbortRun(args[0], author(), reason, force, 10*time.Second)
			if err != nil {
				return err
			}
			if res.Aborted {
				fmt.Printf("■ run %s aborted: %s\n", args[0], res.Message)
				return nil
			}
			fmt.Fprintf(os.Stderr, "run %s: %s\n", args[0], res.Message)
			return ExitError{1}
		},
	}
	cmd.Flags().StringVarP(&reason, "reason", "m", "", "why (recorded with the run)")
	cmd.Flags().BoolVar(&force, "force", false, "mark it aborted now, without waiting for the process running it")
	return cmd
}

func suspendCmd(s func() *store.Store) *cobra.Command {
	var reason string
	var now bool
	cmd := &cobra.Command{
		Use:   "suspend <run-id>",
		Short: "Suspend a run, to resume it later (runnr resume)",
		Long: `Asks the runnr running a run (a runnr run in another terminal, or runnr
serve) to suspend it: it stops once the step running now has finished (or
at once if it's waiting for someone), and ends "suspended", ready for
runnr resume to carry it on. --now stops the running step too (it's done
again on resume). In the terminal running it, [z] at a prompt suspends.

When the process that ran it is gone (it exited on this machine), the run
is marked suspended straight away.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := s().SuspendRun(args[0], &engine.SuspendError{By: author(), Reason: reason, Now: now}, 10*time.Second)
			if err != nil {
				return err
			}
			if res.Suspended {
				fmt.Printf("‖ run %s suspended: %s (resume with: runnr resume %s)\n", args[0], res.Message, args[0])
				return nil
			}
			fmt.Printf("run %s: %s\n", args[0], res.Message)
			return nil
		},
	}
	cmd.Flags().StringVarP(&reason, "reason", "m", "", "why (recorded with the run)")
	cmd.Flags().BoolVar(&now, "now", false, "stop the step running now too, rather than waiting for it to finish")
	return cmd
}

func saveEditsCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "save-edits <run-id>",
		Short: "Save the step changes made during a run as a new runbook version",
		Long: `Steps changed during a run ([c]hange step in the CLI, ✎ edit in the GUI)
apply only to that run until saved. A step that has changed in the runbook
since the run started is a conflict; the YAML is reformatted (comments drop).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := s().LoadRun(args[0])
			if err != nil {
				return err
			}
			rev, err := s().SaveRunEdits(r)
			if err != nil {
				return err
			}
			fmt.Printf("saved %s (version %s)\n", r.Runbook, rev.Short())
			return nil
		},
	}
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
		Long: `Runs only the steps marked 'always' (the setup) and opens a shell (the
runbook's shell, else your $SHELL) with
everything they published, plus inputs and the runbook's env, in the
environment. Nothing is recorded in history.

Given a run ID, the shell uses that run's runbook, inputs and the values its
steps have published so far, as last saved (secret inputs are asked for
again; secret values can't be restored).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTTY(os.Stdin) {
				return errors.New("runnr shell needs a terminal")
			}
			base := map[string]string{}
			var state map[string]string
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
				// What the run has published so far, as it last saved it.
				state = engine.ResumeState(prev)
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
			run := engine.Execute(ctx, rb, term, "", engine.Options{Vars: base, State: state, Interactive: true, ShellOnly: true, Libraries: libs})
			if run.Status != engine.StatusSucceeded {
				return ExitError{1}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&vars, "var", "v", nil, "set an input (key=value), repeatable")
	return cmd
}

// importRuns imports exported run records (only the runs in ids, if any).
func importRuns(st *store.Store, paths, ids []string, check bool) error {
	files, bad := project.ReadRunPaths(paths)
	for _, b := range bad {
		fmt.Fprintf(os.Stderr, "%s: %s\n", b.From, b.Error)
	}
	out, err := project.ImportRuns(st, files, ids, check)
	if err != nil {
		return err
	}
	if len(out) == 0 {
		if len(bad) == 0 {
			return errors.New("no runs to import")
		}
		return ExitError{1}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tRUNBOOK\tSTARTED\tSTATUS\tRESULT")
	n := 0
	for _, r := range out {
		what := "imported"
		switch {
		case r.Exists:
			what = "already here, left as it is"
		case check:
			what = "would import"
		}
		if r.Unfinished && !r.Exists {
			what += " (it was still going: as aborted)"
		}
		if r.FromLog && !r.Exists {
			what += " (from a raw log)"
		}
		if r.Imported || check && !r.Exists {
			n++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Runbook, r.Started.Local().Format("2006-01-02 15:04"), r.Status, what)
	}
	tw.Flush()
	verb := "imported"
	if check {
		verb = "would import"
	}
	fmt.Printf("%s %d of %d run(s)\n", verb, n, len(out))
	if len(bad) > 0 {
		return ExitError{1}
	}
	return nil
}

// scrubRuns masks credentials in stored runs (only those in ids, if any).
func scrubRuns(st *store.Store, ids []string, check bool) error {
	out, err := project.Scrub(st, ids, check)
	n, skipped := 0, 0
	for _, x := range out {
		if x.Err != nil {
			skipped++
			fmt.Printf("%s  %s  left alone: %v\n", x.ID, x.Runbook, x.Err)
			continue
		}
		if !x.Changed() {
			continue
		}
		n++
		var what []string
		if x.Record {
			what = append(what, "record")
		}
		if x.Notes > 0 {
			what = append(what, fmt.Sprintf("%d note(s)", x.Notes))
		}
		if x.Asks > 0 {
			what = append(what, fmt.Sprintf("%d reason(s)", x.Asks))
		}
		fmt.Printf("%s  %s  %s\n", x.ID, x.Runbook, strings.Join(what, ", "))
	}
	verb := "masked credentials in"
	if check {
		verb = "would mask credentials in"
	}
	fmt.Printf("%s %d of %d run(s)", verb, n, len(out))
	if skipped > 0 {
		fmt.Printf("; %d still running, left alone", skipped)
	}
	if n > 0 && !check {
		fmt.Print("; database compacted")
	}
	fmt.Println()
	return err
}

func historyCmd(s func() *store.Store) *cobra.Command {
	var (
		filter string
		limit  int
		asJSON bool
		raw    bool
		del    bool
		export string
		as     string
		single bool
		only   bool
		tl     string
		find   string
		imp    []string
		check  bool
		scrub  bool
	)
	cmd := &cobra.Command{
		Use:   "history [run-id]",
		Short: "List past runs, or show one in detail (--delete removes it)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(imp) > 0 {
				return importRuns(s(), imp, args, check)
			}
			if scrub {
				return scrubRuns(s(), args, check)
			}
			if export != "" {
				if len(args) != 1 {
					return errors.New("--export takes a run id")
				}
				runs, err := project.Chain(s(), args[0])
				if err != nil {
					return err
				}
				if only {
					runs = slices.DeleteFunc(runs, func(r *engine.Run) bool { return r.ID != args[0] })
				}
				files, err := project.LogFiles(project.WithDefinitions(s(), project.Masked(s(), runs)), as, single, "")
				if err != nil {
					return err
				}
				paths, err := project.WriteLogFiles(files, export, false)
				if err != nil {
					return err
				}
				for _, p := range paths {
					fmt.Println("wrote", p)
				}
				return nil
			}
			if del {
				if len(args) != 1 {
					return errors.New("--delete takes a run id")
				}
				if err := s().DeleteRun(args[0]); err != nil {
					return err
				}
				fmt.Println("deleted run", args[0])
				return nil
			}
			if len(args) == 1 {
				r, err := s().LoadRun(args[0])
				if err != nil {
					return err
				}
				// What leaves runnr is scanned for credentials again.
				r = project.MaskedRun(s(), r)
				if asJSON {
					return printJSON(r)
				}
				if raw {
					fmt.Print(engine.RawLog(r))
					return nil
				}
				switch tl {
				case "":
				case "text":
					width := 120
					if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
						width = w
					}
					fmt.Print(timeline.Build(r, time.Now()).Text(width, isTTY(os.Stdout) && os.Getenv("NO_COLOR") == ""))
					return nil
				case "html":
					_, err := os.Stdout.Write(timeline.Build(r, time.Now()).HTML())
					return err
				default:
					return fmt.Errorf("--timeline=%s: text or html", tl)
				}
				printRun(r)
				return nil
			}
			runs, err := s().FindRuns(filter, find, limit)
			if err != nil {
				return err
			}
			if asJSON {
				ps := make([]*engine.Run, len(runs))
				for i := range runs {
					ps[i] = &runs[i]
				}
				return printJSON(project.Masked(s(), ps))
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "RUN ID\tRUNBOOK\tVER\tSTATUS\tMODE\tSTARTED\tDURATION\tFOR")
			for _, r := range runs {
				dur := "-"
				if !r.Ended.IsZero() {
					dur = r.Ended.Sub(r.Started).Round(time.Millisecond).String()
				}
				rev := "-"
				if r.RunbookVersion != "" {
					rev = r.RunbookVersion.Short()
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Runbook, rev, r.Status, r.Mode, r.Started.Local().Format("2006-01-02 15:04:05"), dur, strings.Join(engine.RunIdentifiers(&r, nil), " "))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&filter, "runbook", "r", "", "only runs of this runbook")
	cmd.Flags().BoolVar(&raw, "raw", false, "show the run as a plain log: each step's command, output and result in order")
	cmd.Flags().StringVar(&tl, "timeline", "", "show the run laid out in time: a chart of its steps, then its key moments (--timeline=html: a page to save)")
	cmd.Flags().Lookup("timeline").NoOptDefVal = "text"
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "max runs to list")
	cmd.Flags().StringVarP(&find, "find", "f", "", "only runs whose identifiers (inputs marked identifier: true, say a target) or ID contain this")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.AddCommand(pruneCmd(s))
	cmd.Flags().StringVar(&export, "export", "", "write the run's logs into this folder, with the runs linked to it by resuming")
	cmd.Flags().StringVar(&as, "as", "raw", "--export: raw (logs to read), json (records to import), both, and/or timeline (a page per run laying it out in time), comma-separated")
	cmd.Flags().BoolVar(&single, "one-file", false, "--export: all the runs in one file (per format)")
	cmd.Flags().BoolVar(&only, "only", false, "--export: just this run, not those linked by resuming")
	cmd.Flags().StringArrayVar(&imp, "import", nil, "import exported runs (records or raw logs: .json or .log files, a zip of them, or a folder of them), repeatable; with a run id, just that run")
	cmd.Flags().BoolVar(&check, "check", false, "--import, --scrub: say what it would do, and change nothing")
	cmd.Flags().BoolVar(&scrub, "scrub", false, "mask credentials in stored runs (all of them, or the run given): records, notes and reasons, as exports are masked; then compact the database")
	cmd.Flags().BoolVar(&del, "delete", false, "delete the run's record (not a running one)")
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
	if r.RunbookVersion != "" {
		fmt.Printf("Version: %s\n", r.RunbookVersion)
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
	var libAs string
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
				if _, _, unset := project.Fill(raw, nil); len(unset) > 0 {
					return fmt.Errorf("%s: placeholders without a value: %s (runnr import fills them in: --set NAME=VALUE)", f, strings.Join(unset, ", "))
				}
				if libAs != "" {
					if raw, err = runbook.SetName(raw, libAs); err != nil {
						return fmt.Errorf("%s: %w", f, err)
					}
				}
				var prev ver.ID
				if l, perr := runbook.ParseLibrary(raw); perr == nil {
					if d, lerr := s().LoadLibrary(l.Name); lerr == nil && !d.Builtin {
						prev = d.Version
					}
				}
				lib, rev, err := s().SaveLibrary(raw, store.SaveOpts{Overwrite: force})
				if err != nil {
					if !force && prev != "" {
						err = fmt.Errorf("%w (version %s): --force makes this its current version, --as NAME imports it under another name", err, prev.Short())
					}
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Println(importReport(f, "library "+lib.Name, rev, prev))
			}
			return nil
		},
	}
	importC.Flags().BoolVarP(&force, "force", "f", false, "save over libraries with the same name (as their current version)")
	importC.Flags().StringVar(&libAs, "as", "", "import the library under this name (its name: is changed)")
	var rev string
	showC := &cobra.Command{
		Use: "show <name>", Short: "Print a library's YAML (--version V for an older save)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rev != "" {
				b, _, err := s().RawLibraryVersion(args[0], rev)
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
	showC.Flags().StringVar(&rev, "version", "", "print this saved version (its hash, a prefix of it, or its short form)")
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
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.Name, l.Version.Short(), strings.Join(l.Functions, ", "), desc)
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
				fmt.Fprintln(w, "VERSION\tFIRST SAVED\tBYTES\t")
				for _, v := range vs {
					fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", v.Version.Short(), v.CreatedAt.Local().Format("2006-01-02 15:04:05"), v.Bytes, map[bool]string{true: "current"}[v.Current])
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
func editDoc(noun, name string, raw []byte, save func([]byte) (string, ver.ID, error)) error {
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
		saved, rev, err := save(edited)
		if err == nil {
			fmt.Printf("saved %s %s (version %s)\n", noun, saved, rev.Short())
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
	return editDoc("library", name, doc.Raw, func(raw []byte) (string, ver.ID, error) {
		lib, rev, err := st.SaveLibrary(raw, store.SaveOpts{OldName: name, Base: doc.Version})
		if err != nil {
			return "", "", err
		}
		return lib.Name, rev, nil
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
			return editDoc("collection", args[0], []byte(d.Raw), func(raw []byte) (string, ver.ID, error) {
				return s().SaveCollection(raw, store.SaveOpts{OldName: args[0], Base: d.Version})
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
				name, rev, err := s().SaveCollection(raw, store.SaveOpts{Overwrite: force})
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Printf("imported %s → collection %s (version %s)\n", f, name, rev.Short())
			}
			return nil
		},
	}
	importC.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing collections")
	var create bool
	addC := &cobra.Command{
		Use: "add <collection> <runbook>...", Short: "Add runbooks to a collection (at the end)", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rev, created, err := s().AddToCollection(args[0], create, args[1:]...)
			if err != nil {
				if _, e := s().LoadCollection(args[0]); !create && errors.Is(e, store.ErrNotFound) {
					return fmt.Errorf("%w (--create makes it)", err)
				}
				return err
			}
			fmt.Printf("collection %s%s (version %s): added %s\n", args[0], map[bool]string{true: " created"}[created], rev.Short(), strings.Join(args[1:], ", "))
			return nil
		},
	}
	addC.Flags().BoolVar(&create, "create", false, "create the collection if it doesn't exist")
	var from string
	moveC := &cobra.Command{
		Use:   "move <runbook>... --to <collection>",
		Short: "Move runbooks to another collection",
		Long: `Move runbooks into --to (created if it doesn't exist), out of the collection
they're in. A runbook in several collections needs --from to say which.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			to, _ := cmd.Flags().GetString("to")
			for _, r := range args {
				was, err := s().MoveRunbook(r, from, to, true)
				if err != nil {
					return err
				}
				if was == "" {
					fmt.Printf("%s: added to %s\n", r, to)
				} else {
					fmt.Printf("%s: %s → %s\n", r, was, to)
				}
			}
			return nil
		},
	}
	moveC.Flags().String("to", "", "the collection to move them to")
	moveC.Flags().StringVar(&from, "from", "", "the collection to take them out of (needed if they're in several)")
	_ = moveC.MarkFlagRequired("to")
	cmd.AddCommand(addC, moveC,
		&cobra.Command{
			Use: "remove <collection> <runbook>...", Aliases: []string{"rm-runbook"}, Short: "Take runbooks out of a collection (they stay in runnr)", Args: cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				rev, err := s().RemoveFromCollection(args[0], args[1:]...)
				if err != nil {
					return err
				}
				fmt.Printf("collection %s (version %s): removed %s\n", args[0], rev.Short(), strings.Join(args[1:], ", "))
				return nil
			},
		},
	)
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
				return editDoc("collection", args[0], []byte(d.Raw), func(raw []byte) (string, ver.ID, error) {
					return s().SaveCollection(raw, store.SaveOpts{OldName: args[0], Base: d.Version})
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

// importReport says what importing file did: added it, saved it as a new
// version of the one with its name, or nothing (it was unchanged).
func importReport(file, name string, rev, prev ver.ID) string {
	switch {
	case prev == "":
		return fmt.Sprintf("imported %s → %s (version %s)", file, name, rev.Short())
	case prev == rev:
		return fmt.Sprintf("unchanged %s → %s (version %s)", file, name, rev.Short())
	default:
		return fmt.Sprintf("replaced %s with %s (version %s → %s)", name, file, prev.Short(), rev.Short())
	}
}

func restoreCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "restore [kind name]",
		Short: "List what was deleted, or restore it (runbook, library, fakes, suite, collection)",
		Long: `Deleting keeps a runbook's (library's, fake library's, test suite's,
collection's) versions. Without arguments, restore lists what was deleted;
with a kind and a name, it saves the last version again.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 && len(args) != 2 {
				return errors.New("give a kind and a name, or nothing to list what's deleted")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "KIND\tNAME\tLAST VERSION\tSAVED")
				n := 0
				for _, k := range []string{store.KindRunbook, store.KindLibrary, store.KindFakes, store.KindSuite, store.KindCollection} {
					ds, err := s().ListDeleted(k)
					if err != nil {
						return err
					}
					for _, d := range ds {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Kind, d.Name, d.Version.Short(), d.SavedAt.Local().Format("2006-01-02 15:04"))
						n++
					}
				}
				if n == 0 {
					fmt.Println("nothing deleted")
					return nil
				}
				return w.Flush()
			}
			v, err := s().Restore(args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("restored %s %s (version %s)\n", args[0], args[1], v.Short())
			return nil
		},
	}
}

// exportYAML writes runbook name to file with the libraries chosen.
func exportYAML(st *store.Store, name, file string, libs []string, noLibs, force bool) error {
	if file == "" || file == "-" {
		d, err := st.Load(name)
		if err != nil {
			return err
		}
		os.Stdout.Write(d.Raw)
		return nil
	}
	plan, err := project.ExportPlan(st, name, file)
	if err != nil {
		return err
	}
	if libs == nil && !noLibs {
		for _, f := range plan {
			if f.Kind == project.Library && !f.Builtin {
				libs = append(libs, f.Name)
			}
		}
	}
	if noLibs {
		libs = nil
	}
	written, err := project.Export(st, name, file, libs, force)
	if err != nil {
		return err
	}
	for _, f := range written {
		fmt.Fprintf(os.Stderr, "wrote %s %s → %s\n", f.Kind, f.Name, f.Path)
	}
	for _, f := range plan {
		if f.Kind == project.Library && !slices.Contains(libs, f.Name) {
			fmt.Fprintf(os.Stderr, "left out library %s%s\n", f.Name, map[bool]string{true: " (built into runnr)"}[f.Builtin])
		}
	}
	return nil
}
