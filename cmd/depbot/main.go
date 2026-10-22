// Command depbot keeps dependencies up to date, like Dependabot: it finds
// newer versions of Go modules, GitHub Actions and Dockerfile base images,
// applies them, and opens one pull request per update.
//
//	depbot check                      # list available updates
//	depbot update                     # apply them to the working tree
//	depbot pr --repo owner/name       # open pull requests (needs GITHUB_TOKEN)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/OTA-EITA/depbot/internal/docker"
	"github.com/OTA-EITA/depbot/internal/github"
	"github.com/OTA-EITA/depbot/internal/gomod"
	"github.com/OTA-EITA/depbot/internal/runner"
	"github.com/OTA-EITA/depbot/internal/update"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

var version = "dev"

// deps can be replaced in tests.
type deps struct {
	sources runner.Sources
	prs     runner.PRClient
}

func defaultDeps() deps {
	gh := github.New(os.Getenv("GITHUB_TOKEN"))
	if api := os.Getenv("GITHUB_API_URL"); api != "" {
		gh.BaseURL = api
	}
	return deps{
		sources: runner.Sources{Proxy: gomod.Proxy{URL: goProxy()}, GitHub: gh, Registry: docker.Registry{}},
		prs:     gh,
	}
}

// goProxy returns the first HTTP proxy from $GOPROXY.
func goProxy() string {
	for _, p := range strings.FieldsFunc(os.Getenv("GOPROXY"), func(r rune) bool { return r == ',' || r == '|' }) {
		if strings.HasPrefix(p, "http") {
			return p
		}
	}
	return "https://proxy.golang.org"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

const usage = `usage: depbot <command> [flags] [DIR]

commands:
  check    list available updates
  update   apply updates to the working tree
  pr       open one pull request per update (needs GITHUB_TOKEN)
  version  print the version
`

type common struct {
	ecosystems string
	ignore     multiFlag
	pin        bool
	pinOnly    bool
	output     string
	exitCode   bool
	repo       string
	base       string
	dryRun     bool
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func run(args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd := args[0]
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(stdout, "depbot", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "check", "update", "pr":
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	var c common
	fs := flag.NewFlagSet("depbot "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.ecosystems, "ecosystem", "gomod,github-actions,docker", "comma separated: gomod, github-actions, docker")
	fs.Var(&c.ignore, "ignore", "dependency name to skip; * is a wildcard (repeatable)")
	fs.BoolVar(&c.pin, "pin", false, "pin GitHub Actions to commit SHAs (version kept in a comment)")
	fs.BoolVar(&c.pinOnly, "pin-only", false, "pin GitHub Actions at their current version without bumping anything")
	if cmd == "check" {
		fs.StringVar(&c.output, "output", "table", "output format: table, json or markdown")
		fs.BoolVar(&c.exitCode, "exit-code", false, "exit 1 when updates are available")
	}
	if cmd == "pr" {
		fs.StringVar(&c.repo, "repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name of the repository (or $GITHUB_REPOSITORY)")
		fs.StringVar(&c.base, "base", "main", "base branch for pull requests")
		fs.BoolVar(&c.dryRun, "dry-run", false, "print the pull requests instead of opening them")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	root := "."
	if fs.NArg() > 1 {
		return usageErr(stderr, errors.New("at most one directory"))
	} else if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	eco := map[string]bool{}
	for _, e := range strings.Split(c.ecosystems, ",") {
		e = strings.TrimSpace(e)
		if e != update.GoMod && e != update.Actions && e != update.Docker {
			return usageErr(stderr, fmt.Errorf("unknown ecosystem %q", e))
		}
		eco[e] = true
	}
	var owner, repo string
	if cmd == "pr" {
		var ok bool
		owner, repo, ok = strings.Cut(c.repo, "/")
		if !ok || owner == "" || repo == "" {
			return usageErr(stderr, errors.New("--repo owner/name is required"))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ups, err := runner.Collect(ctx, root, d.sources, runner.Options{Ecosystems: eco, Ignore: c.ignore, Pin: c.pin, PinOnly: c.pinOnly})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitFail
	}

	switch cmd {
	case "check":
		if err := writeUpdates(stdout, ups, c.output); err != nil {
			return usageErr(stderr, err)
		}
		if c.exitCode && len(ups) > 0 {
			return exitFail
		}
	case "update":
		for _, u := range ups {
			if err := runner.Apply(root, u); err != nil {
				fmt.Fprintf(stderr, "error: %s: %v\n", u.Name, err)
				return exitFail
			}
			fmt.Fprintln(stdout, "updated", u.Title())
		}
	case "pr":
		if err := runner.OpenPRs(ctx, root, owner, repo, c.base, d.prs, ups, c.dryRun, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitFail
		}
	}
	return exitOK
}

func usageErr(w io.Writer, err error) int {
	fmt.Fprintln(w, "error:", err)
	return exitUsage
}

func writeUpdates(w io.Writer, ups []update.Update, format string) error {
	switch format {
	case "json":
		if ups == nil {
			ups = []update.Update{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ups)
	case "markdown":
		var b strings.Builder
		fmt.Fprintf(&b, "### depbot: %d update(s) available\n\n", len(ups))
		if len(ups) > 0 {
			b.WriteString("| Ecosystem | Dependency | From | To | Kind |\n|---|---|---|---|---|\n")
			for _, u := range ups {
				fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", u.Ecosystem, u.Name, u.From, u.To, u.Kind)
			}
		}
		_, err := io.WriteString(w, b.String())
		return err
	case "table":
		if len(ups) == 0 {
			_, err := fmt.Fprintln(w, "All dependencies are up to date.")
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ECOSYSTEM\tDEPENDENCY\tFROM\tTO\tKIND\tWHERE")
		for _, u := range ups {
			where := u.Dir
			if len(u.Edits) > 0 {
				var locs []string
				for _, e := range u.Edits {
					locs = append(locs, fmt.Sprintf("%s:%d", e.File, e.Line))
				}
				where = strings.Join(locs, " ")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", u.Ecosystem, u.Name, u.From, u.To, u.Kind, where)
		}
		return tw.Flush()
	default:
		return fmt.Errorf("unknown output format %q (want table, json or markdown)", format)
	}
}
