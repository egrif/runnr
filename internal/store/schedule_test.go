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
			at := time.Date(2026, 11, 4, 22, 0, 0, 0, time.UTC) // a Wednesday
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
			ag, err := st.Agenda(at.Add(-24*time.Hour), time.UTC)
			if err != nil || len(ag) != 1 {
				t.Fatalf("agenda: %v %d", err, len(ag))
			}
			e := ag[0].Entries
			if len(e) != 3 || e[1].Kind != "window" || e[1].End.Sub(e[1].At) != 30*time.Minute ||
				e[0].Kind != "section" || !e[0].At.Equal(at.Add(-time.Hour)) || e[2].Name != "cut" || e[2].Offset != "+12m" {
				t.Fatalf("entries %+v", e)
			}
			all, _ := st.Agenda(time.Time{}, time.UTC)
			if len(all) != 2 || all[0].Runbook != "plain" || all[0].Entries[0].Kind != "start" {
				t.Fatalf("all: %+v", all)
			}
			if err := st.DeleteSchedule(sc.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgendaBusinessDaysAndNotification(t *testing.T) {
	for kind, st := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			rb := `name: wk
maintenance_window: {target: 6h, notification_required: true}
sections: [{name: Prep, window_offset: -1d}, {name: Cut, window_offset: "0"}, {name: After, window_offset: +2d}]
steps:
  - {name: prep, section: Prep, run: x}
  - {name: down, section: Cut, run: x, window: open}
  - {name: mid, run: x, window_offset: +2h}
  - {name: up, run: x, window: close}
  - {name: after, section: After, run: x}
`
			if _, _, err := st.Save([]byte(rb), SaveOpts{}); err != nil {
				t.Fatal(err)
			}
			// Sunday 1 Nov 2026, 02:00 UTC: prep (Saturday) moves to Friday; the
			// window and the step during it stay on Sunday; After (Tuesday) stays;
			// the notification, two weeks before (a Sunday), moves to that Friday.
			at := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
			if err := st.SaveSchedule(&Schedule{Runbook: "wk", At: at}); err != nil {
				t.Fatal(err)
			}
			ag, err := st.Agenda(time.Time{}, time.UTC)
			if err != nil || len(ag) != 1 {
				t.Fatalf("%v %d", err, len(ag))
			}
			got := map[string]string{}
			for _, e := range ag[0].Entries {
				got[e.Kind+":"+e.Name] = e.At.Format("Mon 2 Jan 15:04")
				if e.Planned != nil {
					got[e.Kind+":"+e.Name] += " (was " + e.Planned.Format("Mon") + ")"
				}
			}
			want := map[string]string{
				"notify:Maintenance window notification due": "Fri 16 Oct 02:00 (was Sun)",
				"section:Prep":              "Fri 30 Oct 02:00 (was Sat)",
				"window:Maintenance window": "Sun 1 Nov 02:00",
				"section:Cut":               "Sun 1 Nov 02:00",
				"step:mid":                  "Sun 1 Nov 04:00",
				"section:After":             "Tue 3 Nov 02:00",
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
