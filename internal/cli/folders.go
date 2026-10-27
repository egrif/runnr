package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/egrif/runnr/internal/placeholder"
	"github.com/egrif/runnr/internal/project"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

func foldersCmd(s func() *store.Store) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "folders [runbook]",
		Short: "The folders placeholders (@REPOS_ROOT@, @REPO_ROOT@, @PROJECT_DIR@ …) stand for on this machine",
		Long: `Runbooks and libraries name folders by placeholder, @NAME@, so they're the
same on every machine; each machine sets what they stand for. They're kept
as written, and a run fills them in when it starts. It doesn't start while
one it names has no value, or isn't a folder here.

Values are set for everything (@REPOS_ROOT@: where your repositories are)
or for a collection's runbooks (@REPO_ROOT@, @PROJECT_DIR@), and can name
others, e.g. for a collection:

  runnr folders set REPOS_ROOT=~/dev
  runnr folders set -c payments REPO_ROOT=@REPOS_ROOT@/ops-runbooks \
      PROJECT_DIR=@REPO_ROOT@/projects/db-upgrade

A run takes its runbook's collection's (in several: what they agree on),
or --collection's; --set NAME=FOLDER gives one for that run only.

With no runbook, lists the values set here; given one, the folders a run
of it would have (with --collection and --set as a run takes them).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return showRunFolders(s(), args[0], &f)
			}
			all, err := s().AllPlaceholders()
			if err != nil {
				return err
			}
			if len(all) == 0 {
				fmt.Println("No folders set. runnr folders set NAME=FOLDER (for everything), or -c COLLECTION NAME=FOLDER.")
				return nil
			}
			scopes := slices.Sorted(maps.Keys(all))
			for _, sc := range scopes {
				vals := all[sc]
				ctx := maps.Clone(all[""])
				maps.Copy(ctx, vals)
				full, err := placeholder.Expand(ctx)
				if err != nil {
					return err
				}
				if sc == "" {
					fmt.Println("for everything:")
				} else {
					fmt.Printf("collection %s:\n", sc)
				}
				for _, n := range slices.Sorted(maps.Keys(vals)) {
					printFolder(n, vals[n], full[n])
				}
			}
			return nil
		},
	}
	f.registerFolders(cmd)

	var col string
	set := &cobra.Command{
		Use:   "set NAME=FOLDER...",
		Short: "Set placeholders' folders on this machine (for everything, or -c a collection)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if col != "" {
				if _, err := s().LoadCollection(col); err != nil {
					return fmt.Errorf("collection %q: %w", col, err)
				}
			}
			for _, kv := range args {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || strings.TrimSpace(v) == "" {
					return fmt.Errorf("%q: want NAME=FOLDER (runnr folders unset NAME to remove one)", kv)
				}
				if err := s().SetPlaceholder(col, k, v); err != nil {
					return err
				}
			}
			return nil
		},
	}
	set.Flags().StringVarP(&col, "collection", "c", "", "for this collection's runbooks (default: for everything)")
	unset := &cobra.Command{
		Use:   "unset NAME...",
		Short: "Remove placeholders' folders (for everything, or -c a collection)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, n := range args {
				if err := s().SetPlaceholder(col, n, ""); err != nil {
					return err
				}
			}
			return nil
		},
	}
	unset.Flags().StringVarP(&col, "collection", "c", "", "for this collection's runbooks (default: for everything)")
	cmd.AddCommand(set, unset)
	return cmd
}

// printFolder shows a value as set, what it comes to, and whether that's a
// folder here.
func printFolder(name, raw, full string) {
	mark := "✔"
	if p := placeholder.Check([]string{name}, map[string]string{name: full}); len(p) > 0 {
		mark = "✘ " + p[0].Why
	}
	shown := raw
	if full != raw {
		shown += " → " + full
	}
	fmt.Printf("  @%s@ = %s  %s\n", name, shown, mark)
}

// showRunFolders says what a run of ref would fill its placeholders with.
func showRunFolders(st *store.Store, ref string, f *runFlags) error {
	rb, _, err := st.Resolve(ref)
	if err != nil {
		return err
	}
	libs, err := st.Libraries(rb.Libraries)
	if err != nil {
		return err
	}
	fo, err := f.folderOpts(nil)
	if err != nil {
		return err
	}
	res, err := project.ResolveFolders(st, rb.Name, placeholder.Used(rb, libs), fo)
	if err != nil {
		return err
	}
	return printRunFolders(rb, res)
}

func printRunFolders(rb *runbook.Runbook, res *project.Folders) error {
	if len(res.Used) == 0 {
		fmt.Printf("%s names no placeholders.\n", rb.Name)
		return nil
	}
	switch {
	case res.Collection != "":
		fmt.Printf("%s, with collection %s's folders:\n", rb.Name, res.Collection)
	case len(res.Collections) > 1:
		fmt.Printf("%s is in collections %s (where they agree):\n", rb.Name, strings.Join(res.Collections, ", "))
	default:
		fmt.Printf("%s (in no collection):\n", rb.Name)
	}
	for _, n := range res.Used {
		mark := "✔"
		for _, p := range res.Problems {
			if p.Name == n {
				mark = "✘ " + p.Why
			}
		}
		if v, ok := res.Values[n]; ok {
			fmt.Printf("  @%s@ = %s  (%s) %s\n", n, v, res.From[n], mark)
		} else {
			fmt.Printf("  @%s@  %s\n", n, mark)
		}
	}
	if err := res.Err(); err != nil {
		return ExitError{1}
	}
	return nil
}
