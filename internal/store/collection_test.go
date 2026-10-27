package store

import (
	"slices"
	"strings"
	"testing"
)

func TestCollectionMembership(t *testing.T) {
	for kind, st := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			for _, n := range []string{"a", "b"} {
				if _, _, err := st.Save([]byte(strings.Replace(rbA, "name: a", "name: "+n, 1)), SaveOpts{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := st.AddToCollection("ops", false, "a"); err == nil {
				t.Fatal("adding to a missing collection without create should fail")
			}
			if _, created, err := st.AddToCollection("ops", true, "a", "b"); err != nil || !created {
				t.Fatalf("create: %v %v", created, err)
			}
			if _, _, err := st.AddToCollection("ops", false, "nope"); err == nil {
				t.Fatal("adding a missing runbook should fail")
			}
			// A comment in the collection survives membership changes.
			d, _ := st.LoadCollection("ops")
			raw := strings.Replace(d.Raw, "runbooks:", "# keep me\nrunbooks:", 1)
			if _, _, err := st.SaveCollection([]byte(raw), SaveOpts{OldName: "ops", Base: d.Version}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.MoveRunbook("b", "", "infra", true); err != nil {
				t.Fatal(err)
			}
			ops, _ := st.LoadCollection("ops")
			infra, _ := st.LoadCollection("infra")
			if !slices.Equal(ops.Collection.Runbooks, []string{"a"}) || !slices.Equal(infra.Collection.Runbooks, []string{"b"}) || !strings.Contains(ops.Raw, "# keep me") {
				t.Fatalf("ops=%v infra=%v\n%s", ops.Collection.Runbooks, infra.Collection.Runbooks, ops.Raw)
			}
			// In two collections, a move has to say which one it leaves.
			st.AddToCollection("infra", false, "a")
			if _, err := st.MoveRunbook("a", "", "third", true); err == nil {
				t.Fatal("ambiguous move should fail")
			}
			if from, err := st.MoveRunbook("a", "ops", "third", true); err != nil || from != "ops" {
				t.Fatalf("%q %v", from, err)
			}
			if in, _ := st.CollectionsOf("a"); !slices.Equal(in, []string{"infra", "third"}) {
				t.Fatalf("a is in %v", in)
			}
			if _, err := st.RemoveFromCollection("infra", "a", "b"); err != nil {
				t.Fatal(err)
			}
			if c, _ := st.LoadCollection("infra"); len(c.Collection.Runbooks) != 0 || !strings.Contains(c.Raw, "runbooks: []") {
				t.Fatalf("%s", c.Raw)
			}
		})
	}
}

func TestCollectionEditsInPlace(t *testing.T) {
	for kind, st := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			for _, n := range []string{"a", "b", "c"} {
				st.Save([]byte(strings.Replace(rbA, "name: a", "name: "+n, 1)), SaveOpts{})
			}
			raw := "name: ops\n# about ops\ndescription: old\nrunbooks: [a, b]\n"
			if _, _, err := st.SaveCollection([]byte(raw), SaveOpts{}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SetCollectionRunbooks("ops", []string{"b", "a", "c"}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SetCollectionRunbooks("ops", []string{"b", "nope"}); err == nil {
				t.Fatal("unknown runbook should fail")
			}
			desc, exp, nn := "new\ntwo lines", "all done", "ops2"
			name, _, err := st.UpdateCollection("ops", &nn, &desc, &exp)
			if err != nil || name != "ops2" {
				t.Fatalf("%q %v", name, err)
			}
			d, err := st.LoadCollection("ops2")
			if err != nil {
				t.Fatal(err)
			}
			c := d.Collection
			if c.Description != "new\ntwo lines" || c.Expect != "all done" || !slices.Equal(c.Runbooks, []string{"b", "a", "c"}) || !strings.Contains(d.Raw, "# about ops") {
				t.Fatalf("%+v\n%s", c, d.Raw)
			}
			empty := ""
			st.UpdateCollection("ops2", nil, nil, &empty)
			if d, _ := st.LoadCollection("ops2"); d.Collection.Expect != "" || strings.Contains(d.Raw, "expect") {
				t.Fatalf("expect not removed:\n%s", d.Raw)
			}
		})
	}
}
