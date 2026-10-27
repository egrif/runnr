package engine

import (
	"errors"
	"strings"

	"github.com/egrif/runnr/internal/placeholder"
	"github.com/egrif/runnr/internal/runbook"
)

// ErrFolders is a run that can't start: a placeholder it names (@REPO_ROOT@
// …) has no folder on this machine.
var ErrFolders = errors.New("placeholders without a folder here")

// fillFolders fills in the placeholders rb, its libraries and the inputs
// given name with opts.Folders, in place, and returns those it filled.
// One without a value, or whose value isn't a folder here, is an error
// (wrapping ErrFolders), and nothing is filled.
func fillFolders(rb *runbook.Runbook, opts Options) (map[string]string, error) {
	used := placeholder.Used(rb, opts.Libraries, opts.Vars)
	if len(used) == 0 {
		return nil, nil
	}
	if probs := placeholder.Check(used, opts.Folders); len(probs) > 0 {
		return nil, FoldersError(probs)
	}
	placeholder.Apply(opts.Folders, rb, opts.Libraries, opts.Vars)
	out := map[string]string{}
	for _, n := range used {
		out[n] = opts.Folders[n]
	}
	return out, nil
}

// FoldersError says why a run can't start with these placeholders.
func FoldersError(probs []placeholder.Problem) error {
	var lines []string
	for _, p := range probs {
		lines = append(lines, p.String())
	}
	return &foldersError{strings.Join(lines, "; ")}
}

type foldersError struct{ msg string }

func (e *foldersError) Error() string {
	return "can't start: " + e.msg + " (give each a folder on this machine: runnr folders set, or --set NAME=FOLDER for this run)"
}
func (e *foldersError) Unwrap() error { return ErrFolders }
