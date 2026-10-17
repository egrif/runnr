package store

import (
	"testing"
	"time"
)

func TestSchedules(t *testing.T) {
	for kind, st := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			rb := `name: mig
inputs: [{name: env, choices: [stg, prd]}, {name: t_cut}, {name: pw, secret: true}]
maintenance_window: {target: 30m}
sections: [{name: Prep, window_offset: -1h}]
steps:
  - {name: prep, section: Prep, run: x}
  - {name: down, run: x, window: open}
  - {name: cut, run: x, window_offset: +5m, offset_input: t_cut}
  - {name: up, run: x, window: close}
`
			if _, _, err := st.Save([]byte(rb), SaveOpts{}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.Save([]byte("name: plain\nsteps: [{name: s, run: x}]\n"), SaveOpts{}); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
			bad := &Schedule{Runbook: "mig", At: at, Inputs: map[string]string{"env": "dev"}}
			if err := st.SaveSchedule(bad); err == nil {
				t.Fatal("an input outside its choices should fail")
			}
			sc := &Schedule{Runbook: "mig", At: at, Title: "prd cutover", Inputs: map[string]string{"env": "prd", "t_cut": "+12m", "pw": "secret"}}
			if err := st.SaveSchedule(sc); err != nil {
				t.Fatal(err)
			}
			if got, _ := st.GetSchedule(sc.ID); got.Inputs["pw"] != "" || got.Title != "prd cutover" {
				t.Fatalf("secret kept or title lost: %+v", got)
			}
			st.SaveSchedule(&Schedule{Runbook: "plain", At: at.Add(-48 * time.Hour)})
			ag, err := st.Agenda(at.Add(-24 * time.Hour))
			if err != nil || len(ag) != 1 {
				t.Fatalf("agenda: %v %d", err, len(ag))
			}
			e := ag[0].Entries
			if len(e) != 3 || e[1].Kind != "window" || e[1].End.Sub(e[1].At) != 30*time.Minute ||
				e[0].Kind != "section" || !e[0].At.Equal(at.Add(-time.Hour)) || e[2].Name != "cut" || e[2].Offset != "+12m" {
				t.Fatalf("entries %+v", e)
			}
			all, _ := st.Agenda(time.Time{})
			if len(all) != 2 || all[0].Runbook != "plain" || all[0].Entries[0].Kind != "start" {
				t.Fatalf("all: %+v", all)
			}
			if err := st.DeleteSchedule(sc.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
