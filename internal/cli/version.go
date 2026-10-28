package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/egrif/runnr/internal/server"
	"github.com/spf13/cobra"
)

// buildInfo is what Go recorded about this build: the module's version
// (go install …@v1.2.3), and for a build from a checkout the commit, when
// it was made, and whether the checkout had changes.
type buildInfo struct {
	module, commit, when string
	modified             bool
}

func readBuildInfo() buildInfo {
	var b buildInfo
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		b.module = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			b.commit = s.Value
		case "vcs.time":
			b.when = s.Value
		case "vcs.modified":
			b.modified = s.Value == "true"
		}
	}
	return b
}

// Version is runnr's version: as stamped by the release build and make
// (git describe: v1.2.3, or v1.2.3-4-gabc1234-dirty between releases),
// else the module version go install …@v1.2.3 records, else "dev" with
// the commit it was built from.
func Version() string {
	if version != "dev" && version != "" {
		return version
	}
	b := readBuildInfo()
	if b.module != "" {
		return b.module
	}
	if b.commit == "" {
		return "dev"
	}
	v := "dev-" + short(b.commit)
	if b.modified {
		v += "-dirty"
	}
	return v
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// serverBuild is this runnr's version and build, for the GUI's About.
func serverBuild() server.Build {
	b := readBuildInfo()
	return server.Build{Version: Version(), Commit: b.commit, Committed: b.when, Modified: b.modified}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print runnr's version, and the commit and Go it was built with",
		Args:  cobra.NoArgs,
		// It needs no database.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Println("runnr " + Version())
			b := readBuildInfo()
			var built []string
			if b.commit != "" {
				c := "commit " + short(b.commit)
				if b.modified {
					c += " (with uncommitted changes)"
				}
				built = append(built, c)
			}
			if b.when != "" {
				built = append(built, "committed "+b.when)
			}
			built = append(built, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
			fmt.Println(strings.Join(built, " · "))
		},
	}
}
