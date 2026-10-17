package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

func scheduleCmd(s func() *store.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "schedule",
		Aliases: []string{"schedules", "agenda"},
		Short:   "Plan runs ahead of time and see them as an agenda",
		Long: `Plan runs ahead of time. For a runbook with a maintenance window, --at is
when the window is to open (T+0); its sections and steps with planned
offsets are laid out from there. Nothing starts on its own (yet): it's for
planning, and for seeing what's coming.`,
	}
	var at, title, notes string
	var vars []string
	add := &cobra.Command{
		Use: "add <runbook> --at <time>", Short: "Schedule a run", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseWhen(at)
			if err != nil {
				return err
			}
			in := map[string]string{}
			for _, kv := range vars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--var %q must be key=value", kv)
				}
				in[k] = v
			}
			sc := &store.Schedule{Runbook: args[0], At: t, Title: title, Notes: notes, Inputs: in, CreatedBy: author()}
			if err := s().SaveSchedule(sc); err != nil {
				return err
			}
			fmt.Printf("scheduled %s for %s (%s)\n", sc.Runbook, sc.At.Local().Format("Mon 2 Jan 2006 15:04 MST"), sc.ID)
			return nil
		},
	}
	add.Flags().StringVar(&at, "at", "", `when (local time): "2026-11-01 22:00"; for a maintenance window, when it opens`)
	add.Flags().StringVar(&title, "title", "", "what it is, e.g. prod-s101 cutover")
	add.Flags().StringVar(&notes, "notes", "", "anything to remember")
	add.Flags().StringArrayVarP(&vars, "var", "v", nil, "an input for the run (key=value; inputs can override planned offsets)")
	_ = add.MarkFlagRequired("at")

	var detail string
	var next, past bool
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "Show scheduled runs as an agenda", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level := map[string]int{"none": 0, "windows": 0, "sections": 1, "steps": 2, "all": 2}
			lv, ok := level[detail]
			if !ok {
				return errors.New("--detail must be windows, sections or steps")
			}
			from := time.Now()
			if past {
				from = time.Time{}
			}
			ag, err := s().Agenda(from)
			if err != nil {
				return err
			}
			if next {
				ag = nextActions(ag, lv, time.Now())
			}
			if len(ag) == 0 {
				fmt.Println("Nothing scheduled. Add one with: runnr schedule add <runbook> --at \"2026-11-01 22:00\"")
				return nil
			}
			printAgenda(ag, lv)
			return nil
		},
	}
	list.Flags().StringVar(&detail, "detail", "steps", "what to show of each run: windows, sections or steps")
	list.Flags().BoolVar(&next, "next", false, "only the next action of each run still to come")
	list.Flags().BoolVar(&past, "past", false, "include runs that are over")

	rm := &cobra.Command{
		Use: "rm <id>", Aliases: []string{"delete"}, Short: "Remove a scheduled run", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s().DeleteSchedule(args[0]); err != nil {
				return err
			}
			fmt.Println("removed", args[0])
			return nil
		},
	}
	cmd.AddCommand(add, list, rm)
	return cmd
}

// nextActions keeps, for each run that isn't over, just its next action at
// this level of detail (or its window, if that's open now).
func nextActions(ag []store.PlannedRun, level int, now time.Time) []store.PlannedRun {
	shown := func(e store.AgendaEntry) bool {
		return e.Kind == "window" || e.Kind == "start" || (e.Kind == "section" && level >= 1) || (e.Kind == "step" && level >= 2)
	}
	var out []store.PlannedRun
	for _, pr := range ag {
		var keep *store.AgendaEntry
		for _, e := range pr.Entries {
			if shown(e) && !e.At.Before(now) {
				keep = &e
				break
			}
		}
		if keep == nil {
			for _, e := range pr.Entries {
				if e.Kind == "window" && e.End != nil && !e.End.Before(now) {
					e.Name = "open now"
					keep = &e
				}
			}
		}
		if keep != nil {
			pr.Entries = []store.AgendaEntry{*keep}
			out = append(out, pr)
		}
	}
	return out
}

// parseWhen reads a local date and time.
func parseWhen(v string) (time.Time, error) {
	for _, f := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(f, strings.TrimSpace(v), time.Local); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--at %q: use local time like \"2026-11-01 22:00\" (or RFC 3339)", v)
}

// printAgenda prints scheduled runs by day: each run, then (by level) its
// window, sections and steps at their planned times.
func printAgenda(ag []store.PlannedRun, level int) {
	t := NewTerminal(0)
	if !isTTY(os.Stdout) {
		t.color = false
	}
	day := ""
	line := func(at time.Time, text string) {
		if d := at.Local().Format("Mon 2 Jan 2006"); d != day {
			day = d
			fmt.Println(t.c("1", "\n"+d))
		}
		fmt.Printf("  %s  %s\n", t.c("36", at.Local().Format("15:04")), text)
	}
	for _, pr := range ag {
		name := pr.Runbook
		if pr.Title != "" {
			name += " — " + pr.Title
		}
		first := pr.At
		for _, e := range pr.Entries {
			if e.At.Before(first) {
				first = e.At
			}
		}
		head := t.c("1", name) + t.c("2", "  ("+pr.ID+")")
		if pr.Error != "" {
			head += t.c("31", "  "+pr.Error)
		}
		line(first, head)
		for _, e := range pr.Entries {
			switch {
			case e.Kind == "window":
				w := "🔴 maintenance window opens"
				if e.Name == "open now" {
					w = "🔴 maintenance window is open now"
				}
				if e.End != nil {
					until := e.End.Local().Format("15:04")
					if e.End.Local().YearDay() != e.At.Local().YearDay() || e.End.Year() != e.At.Year() {
						until = e.End.Local().Format("Mon 2 Jan 15:04")
					}
					w += fmt.Sprintf(" · until %s (%s)", until, runbook.FormatSpan(e.End.Sub(e.At)))
				}
				line(e.At, t.c("31", "  "+w))
			case e.Kind == "section" && level >= 1:
				line(e.At, t.c("35", "  ▸ "+e.Name)+t.c("2", " T"+e.Offset))
			case e.Kind == "step" && level >= 2:
				line(e.At, fmt.Sprintf("    · %d. %s", e.Step+1, e.Name)+t.c("2", " T"+e.Offset))
			}
		}
	}
}
