// Package cli implements the runnr command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
	var home string
	var st *store.Store
	root := &cobra.Command{
		Use:           "runnr",
		Short:         "Create, manage and run runbooks — interactively or unattended",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			st, err = store.New(home)
			return err
		},
	}
	root.PersistentFlags().StringVar(&home, "home", store.DefaultHome(), "runnr data directory (env RUNNR_HOME)")
	s := func() *store.Store { return st }

	root.AddCommand(
		listCmd(s), showCmd(s), newCmd(s), editCmd(s), importCmd(s), deleteCmd(s),
		validateCmd(s), runCmd(s), resumeCmd(s), historyCmd(s), serveCmd(s),
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
			fmt.Fprintln(w, "NAME\tSTEPS\tDESCRIPTION")
			for _, e := range entries {
				desc := e.Description
				if e.Err != "" {
					desc = "INVALID: " + firstLine(e.Err)
				}
				fmt.Fprintf(w, "%s\t%d\t%s\n", e.Name, e.Steps, firstLine(desc))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func showCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print a runbook's YAML",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := s().Raw(args[0])
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(b)
			return err
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
			if _, err := s().Save(raw, ""); err != nil {
				return err
			}
			fmt.Println("created", s().Path(name))
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
			if _, err := s().Raw(args[0]); err != nil {
				return err
			}
			return editLoop(s(), args[0])
		},
	}
}

// editLoop edits a temp copy and only replaces the stored runbook once valid.
func editLoop(st *store.Store, name string) error {
	raw, err := st.Raw(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "runnr-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
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
		rb, err := st.Save(edited, name)
		if err == nil {
			fmt.Println("saved", st.Path(rb.Name))
			return nil
		}
		fmt.Fprintln(os.Stderr, "invalid runbook:\n"+indent(err.Error()))
		a, err := t.choose(context.Background(), "[e]dit again / [d]iscard changes:", map[string]string{"e": "e", "d": "d", "": "e"}, "e")
		if err != nil || a == "d" {
			return errors.New("changes discarded")
		}
	}
}

func importCmd(s func() *store.Store) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "import <file>...",
		Short: "Validate and copy runbook files into the library",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var failed bool
			for _, f := range args {
				raw, err := os.ReadFile(f)
				if err == nil {
					var rb *runbook.Runbook
					if rb, err = runbook.Parse(raw); err == nil {
						if s().Exists(rb.Name) && !force {
							err = fmt.Errorf("runbook %q exists (use --force)", rb.Name)
						} else {
							_, err = s().Save(raw, "")
						}
					}
					if err == nil {
						fmt.Printf("imported %s → %s\n", f, rb.Name)
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
				if _, err := s().Resolve(ref); err != nil {
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
}

// execute runs rb with these flags; base holds inputs that --var overrides.
func (f *runFlags) execute(st *store.Store, rb *runbook.Runbook, base map[string]string, startAt int, resumes string) error {
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
	var ui engine.Interactor = term
	if !interactive {
		onEvent := term.Notify
		if f.js {
			onEvent = nil
		}
		ui = engine.Unattended{OnEvent: onEvent, AssumeYes: f.yes}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := engine.Options{Vars: values, DryRun: f.dry, Interactive: interactive, StartAt: startAt, Resumes: resumes}
	if !f.noHistory {
		opts.Save = func(r *engine.Run) {
			if err := st.SaveRun(r.ID, r); err != nil {
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
			rb, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			start := 0
			if from != "" {
				if start, err = rb.StepIndex(from); err != nil {
					return err
				}
			}
			return f.execute(s(), rb, nil, start, "")
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&from, "from", "", "start at this step (id, number or name)")
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
			var prev engine.Run
			if err := s().LoadRun(args[0], &prev); err != nil {
				return err
			}
			start := engine.ResumePoint(&prev)
			if start < 0 {
				return fmt.Errorf("run %s has no unfinished steps", prev.ID)
			}
			rb, err := s().Get(prev.Runbook)
			if err != nil {
				return err
			}
			if len(rb.Steps) != len(prev.Steps) || rb.Steps[start].Name != prev.Steps[start].Name {
				return fmt.Errorf("runbook %q changed since run %s; use: runnr run %s --from <step>", rb.Name, prev.ID, rb.Name)
			}
			fmt.Printf("resuming %s at step %d (%s)\n", prev.ID, start+1, rb.Steps[start].Name)
			return f.execute(s(), rb, engine.ResumeInputs(&prev), start, prev.ID)
		},
	}
	f.register(cmd)
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
				var r engine.Run
				if err := s().LoadRun(args[0], &r); err != nil {
					return err
				}
				if asJSON {
					return printJSON(r)
				}
				printRun(&r)
				return nil
			}
			ids, err := s().RunIDs()
			if err != nil {
				return err
			}
			var runs []engine.Run
			for _, id := range ids {
				if len(runs) >= limit {
					break
				}
				var r engine.Run
				if s().LoadRun(id, &r) != nil || (filter != "" && r.Runbook != filter) {
					continue
				}
				runs = append(runs, r)
			}
			if asJSON {
				return printJSON(runs)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "RUN ID\tRUNBOOK\tSTATUS\tMODE\tSTARTED\tDURATION")
			for _, r := range runs {
				dur := "-"
				if !r.Ended.IsZero() {
					dur = r.Ended.Sub(r.Started).Round(time.Millisecond).String()
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Runbook, r.Status, r.Mode, r.Started.Format("2006-01-02 15:04:05"), dur)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&filter, "runbook", "r", "", "only runs of this runbook")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "max runs to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func printRun(r *engine.Run) {
	fmt.Printf("Run %s — %s (%s)\n", r.ID, r.Runbook, r.Mode)
	fmt.Printf("Status:  %s\n", r.Status)
	if r.Error != "" {
		fmt.Printf("Error:   %s\n", r.Error)
	}
	fmt.Printf("Started: %s\n", r.Started.Format(time.RFC3339))
	for k, v := range r.Inputs {
		fmt.Printf("Input:   %s=%s\n", k, v)
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

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
