package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

func doctorCmd(s func() *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor <runbook|file>",
		Short: "Check that the programs a runbook needs (its requires, and its libraries') are here",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rb, _, err := s().Resolve(args[0])
			if err != nil {
				return err
			}
			libs, err := s().Libraries(rb.Libraries)
			if err != nil {
				return err
			}
			reqs := runbook.Requirements(rb, libs)
			if len(reqs) == 0 {
				fmt.Printf("%s doesn't list any requirements (requires:)\n", rb.Name)
				return nil
			}
			t := NewTerminal(0)
			bad := 0
			for _, r := range runbook.CheckRequirements(reqs) {
				what := r.Cmd
				if r.Found != "" {
					what += " " + r.Found
				}
				why := ""
				if r.Why != "" {
					why = t.c("2", "  ("+r.Why+")")
				}
				switch {
				case r.Problem == "":
					fmt.Printf("  %s %s%s\n", t.c("32", "✔"), what, why)
				case r.Optional:
					fmt.Printf("  %s %s%s\n", t.c("33", "◇"), r.Problem, why)
				default:
					bad++
					fmt.Printf("  %s %s%s\n", t.c("31", "✘"), r.Problem, why)
				}
				if r.Problem != "" && r.Install != "" {
					fmt.Printf("      %s\n", t.c("2", r.Install))
				}
			}
			if bad > 0 {
				return ExitError{1}
			}
			return nil
		},
	}
}
