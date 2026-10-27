package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/project"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/runtest"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

func testCmd(s func() *store.Store) *cobra.Command {
	var only, fakeFiles, libFiles []string
	var rbFile string
	var verbose, keep, asJSON, showLog bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "test <suite|runbook|file.yaml>",
		Short: "Test a runbook: run it with commands faked and a simulated operator",
		Long: `Runs a runbook's test suite: each scenario runs the real runbook with the
commands it names faked (canned responses, from fake libraries and the
suite), a simulated operator answering its prompts, and expectations
about the run (statuses, calls made or not made, their order, output).

Commands listed under isolate never run for real: a call to one that no
fake answers fails the scenario. Retries and repeats don't really wait.

The argument is a stored suite (runnr tests import), a runbook whose
suite is named after it, or a suite file. --fakes adds fake libraries
from files, and --lib function libraries (both win over stored ones of
the same name), so a runbook in a repo can be tested without importing it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			suite, err := loadSuite(s(), args[0])
			if err != nil {
				return err
			}
			files := map[string]*runtest.Library{}
			for _, f := range fakeFiles {
				raw, err := store.ReadFilled(f)
				if err != nil {
					return err
				}
				l, err := runtest.ParseLibrary(raw)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				files[l.Name] = l
			}
			libs := map[string]*runbook.Library{}
			for _, f := range libFiles {
				raw, err := store.ReadFilled(f)
				if err != nil {
					return err
				}
				l, err := runbook.ParseLibrary(raw)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				libs[l.Name] = l
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			var draft *runbook.Runbook
			if rbFile != "" {
				raw, err := store.ReadFilled(rbFile)
				if err != nil {
					return err
				}
				if draft, err = runbook.Parse(raw); err != nil {
					return fmt.Errorf("%s: %w", rbFile, err)
				}
			}
			src := runtest.Sources{
				Runbook: func(name string) (*runbook.Runbook, error) {
					if draft != nil {
						return draft, nil
					}
					rb, _, err := s().Get(name)
					return rb, err
				},
				Fakes: func(name string) (*runtest.Library, error) {
					if l, ok := files[name]; ok {
						return l, nil
					}
					return s().Fakes(name)
				},
				Libraries: func(names []string) ([]*runbook.Library, error) {
					var out []*runbook.Library
					for _, n := range names {
						if l, ok := libs[n]; ok {
							out = append(out, l)
							continue
						}
						l, err := s().Libraries([]string{n})
						if err != nil {
							return nil, err
						}
						out = append(out, l...)
					}
					return out, nil
				},
			}
			results, err := runtest.Run(cmd.Context(), suite, src, runtest.Options{Only: only, Keep: keep, Exe: exe, Timeout: timeout})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(results)
			}
			failed := printResults(suite, results, verbose, showLog)
			if failed > 0 {
				return ExitError{1}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&only, "scenario", "s", nil, "run only this scenario (repeatable)")
	cmd.Flags().StringArrayVar(&fakeFiles, "fakes", nil, "a fake library file to use (repeatable)")
	cmd.Flags().StringArrayVar(&libFiles, "lib", nil, "a function library file to use (repeatable)")
	cmd.Flags().StringVar(&rbFile, "runbook", "", "test this runbook file instead of the saved runbook (e.g. a change before saving it)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show every call, the operator's answers and unused fakes")
	cmd.Flags().BoolVar(&showLog, "log", false, "show each scenario's run as a plain log")
	cmd.Flags().BoolVar(&keep, "keep", false, "keep each scenario's directory (fakes, call log)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "stop a scenario that runs longer than this")
	return cmd
}

// loadSuite finds a suite: a file, or a stored suite by name.
func loadSuite(st *store.Store, arg string) (*runtest.Suite, error) {
	if _, err := os.Stat(arg); err == nil {
		raw, err := store.ReadFilled(arg)
		if err != nil {
			return nil, err
		}
		s, err := runtest.ParseSuite(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", arg, err)
		}
		return s, nil
	}
	d, err := st.LoadSuite(arg)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("no test suite %q (runnr tests list; runnr tests import <file>)", arg)
		}
		return nil, err
	}
	if d.FillErr != nil {
		return nil, d.FillErr
	}
	return runtest.ParseSuite(d.Filled)
}

// printResults reports each scenario and returns how many failed.
func printResults(suite *runtest.Suite, results []runtest.Result, verbose, showLog bool) int {
	t := NewTerminal(0)
	failed := 0
	mode := ""
	if suite.Strict {
		mode = ", sandboxed"
	}
	fmt.Printf("%s  %s\n", t.c("1", suite.Name), t.c("2", fmt.Sprintf("(runbook %s, %d scenario(s)%s)", suite.Runbook, len(results), mode)))
	if len(results) > 0 {
		for _, w := range results[0].Warnings { // the same for every scenario
			fmt.Printf("  %s %s\n", t.c("33", "warning:"), w)
		}
	}
	for _, r := range results {
		mark := t.c("32", "✔")
		if !r.Passed {
			mark, failed = t.c("31", "✘"), failed+1
		}
		fmt.Printf("  %s %s  %s\n", mark, r.Scenario, t.c("2", fmt.Sprintf("run %s · %d calls · %s", r.Run.Status, len(r.Calls), r.Took.Round(time.Millisecond))))
		for _, p := range r.Problems {
			fmt.Printf("      %s\n", t.c("31", p))
		}
		if verbose {
			for _, c := range r.Calls {
				how := "fake (" + c.From + ")"
				switch {
				case c.Real:
					how = "ran for real"
				case c.Unfaked:
					how = t.c("31", "refused: no fake")
				}
				step := "      "
				if c.Step > 0 {
					step = fmt.Sprintf("[%3d] ", c.Step)
				}
				fmt.Printf("      %s%s  %s\n", t.c("2", step), c.Line(), t.c("2", fmt.Sprintf("→ %s, exit %d", how, c.Exit)))
			}
			if len(r.Asked) > 0 {
				fmt.Printf("      %s %s\n", t.c("2", "operator:"), strings.Join(r.Asked, "; "))
			}
			for _, u := range r.Unused {
				fmt.Printf("      %s %s\n", t.c("33", "unused fake:"), u)
			}
			if r.Dir != "" {
				fmt.Printf("      %s %s\n", t.c("2", "kept:"), r.Dir)
			}
		}
		if showLog {
			fmt.Println(indentBy(engine.RawLog(r.Run), "      | "))
		}
	}
	fmt.Printf("%d passed, %d failed\n", len(results)-failed, failed)
	return failed
}

// testsCmd manages stored test suites; fakesCmd, fake libraries.
func testsCmd(s func() *store.Store) *cobra.Command {
	return docCmd("tests", "test suite", "Manage runbooks' test suites",
		func(raw []byte, force bool, o *store.Origin) (string, ver.ID, error) {
			return s().SaveSuite(raw, store.SaveOpts{Overwrite: force, Origin: o})
		},
		func() ([]store.TestDoc, error) { return s().ListSuites() },
		func(n string) (*store.TestDoc, error) { return s().LoadSuite(n) },
		func(n string) error { return s().DeleteSuite(n) },
		func(d store.TestDoc) string {
			if su, err := runtest.ParseSuite(d.Raw); err == nil {
				names := make([]string, len(su.Scenarios))
				for i, sc := range su.Scenarios {
					names[i] = sc.Name
				}
				return "runbook " + su.Runbook + ": " + strings.Join(names, ", ")
			}
			return "(invalid)"
		})
}

func fakesCmd(s func() *store.Store) *cobra.Command {
	cmd := docCmd("fakes", "fake library", "Manage fake libraries (canned command responses for tests)",
		func(raw []byte, force bool, o *store.Origin) (string, ver.ID, error) {
			return s().SaveFakes(raw, store.SaveOpts{Overwrite: force, Origin: o})
		},
		func() ([]store.TestDoc, error) { return s().ListFakes() },
		func(n string) (*store.TestDoc, error) { return s().LoadFakes(n) },
		func(n string) error { return s().DeleteFakes(n) },
		func(d store.TestDoc) string {
			if l, err := runtest.ParseLibrary(d.Raw); err == nil {
				return fmt.Sprintf("%d fakes  %s", len(l.Fakes), l.Description)
			}
			return "(invalid)"
		})
	cmd.AddCommand(&cobra.Command{
		Use:   "check <name|file|suite>...",
		Short: "Check AWS CLI fakes against the responses the CLI describes",
		Long: `Compares the JSON of every AWS CLI fake with the response the AWS CLI itself
describes for that operation (aws SERVICE OPERATION --generate-cli-skeleton
output, which needs no credentials), and lists fields the real response
doesn't have: a misspelt or made-up field would make the runbook's queries
pass in a test and fail for real. Takes fake libraries (stored, or files)
and test suites (their own fakes).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bad := 0
			for _, a := range args {
				fakes, err := fakesOf(s(), a)
				if err != nil {
					return err
				}
				probs, notes := runtest.CheckAWS(fakes, "")
				for _, n := range notes {
					defer fmt.Printf("    note: %s\n", n)
				}
				if len(probs) == 0 {
					fmt.Printf("✔ %s\n", a)
					continue
				}
				bad++
				fmt.Printf("✘ %s\n", a)
				for _, p := range probs {
					fmt.Printf("    %s\n", p)
				}
			}
			if bad > 0 {
				return ExitError{1}
			}
			return nil
		},
	})
	return cmd
}

// fakesOf is the fakes in a library or suite (a file, or stored).
func fakesOf(st *store.Store, arg string) ([]runtest.Fake, error) {
	raw, err := os.ReadFile(arg)
	if err != nil {
		if d, e := st.LoadFakes(arg); e == nil {
			raw = d.Filled
		} else if d, e := st.LoadSuite(arg); e == nil {
			raw = d.Filled
		} else {
			return nil, fmt.Errorf("%s: not a file, fake library or test suite", arg)
		}
	}
	if l, err := runtest.ParseLibrary(raw); err == nil {
		return l.Fakes, nil
	}
	su, err := runtest.ParseSuite(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", arg, err)
	}
	fakes := su.Fakes
	for _, sc := range su.Scenarios {
		fakes = append(fakes, sc.Fakes...)
	}
	return fakes, nil
}

// docCmd is list / show / import / delete for a kind of stored document.
func docCmd(use, noun, short string,
	save func([]byte, bool, *store.Origin) (string, ver.ID, error),
	list func() ([]store.TestDoc, error),
	load func(string) (*store.TestDoc, error),
	del func(string) error,
	summary func(store.TestDoc) string,
) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short}
	var force bool
	var sets []string
	importC := &cobra.Command{
		Use: "import <file>...", Short: "Add " + noun + " files (- reads stdin)", Args: cobra.MinimumNArgs(1),
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
				path := f
				if f == "-" {
					path = ""
				}
				set, err := setValues(sets)
				if err != nil {
					return err
				}
				origin, unset, err := project.OriginFor(path, raw, set)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				if len(unset) > 0 {
					return fmt.Errorf("%s: placeholders without a value: %s (--set NAME=VALUE)", f, strings.Join(unset, ", "))
				}
				name, rev, err := save(raw, force, origin)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				fmt.Printf("imported %s → %s %s (version %s)\n", f, noun, name, rev.Short())
			}
			return nil
		},
	}
	importC.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing ones")
	importC.Flags().StringArrayVar(&sets, "set", nil, "a placeholder's value (NAME=VALUE, for @NAME@)")
	var asJSON bool
	listC := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List " + plural(noun), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			docs, err := list()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(docs)
			}
			if len(docs) == 0 {
				fmt.Printf("none yet: runnr %s import <file>\n", use)
			}
			for _, d := range docs {
				fmt.Printf("%-28s %s  %s\n", d.Name, d.Version.Short(), summary(d))
			}
			return nil
		},
	}
	listC.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	showC := &cobra.Command{
		Use: "show <name>", Short: "Print a " + noun + "'s YAML", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := load(args[0])
			if err != nil {
				return err
			}
			os.Stdout.Write(d.Raw)
			return nil
		},
	}
	delC := &cobra.Command{
		Use: "delete <name>", Aliases: []string{"rm"}, Short: "Delete a " + noun, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := del(args[0]); err != nil {
				return err
			}
			fmt.Println("deleted", args[0])
			return nil
		},
	}
	cmd.AddCommand(importC, listC, showC, delC)
	return cmd
}

func indentBy(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

// plural is a noun's plural, for help text ("fake library" → "fake libraries").
func plural(noun string) string {
	if strings.HasSuffix(noun, "y") {
		return strings.TrimSuffix(noun, "y") + "ies"
	}
	return noun + "s"
}
