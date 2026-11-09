// tmpfs-cli uploads and downloads files on tmpfs.it with end-to-end
// encryption, compatible with the webapp. See STEERING.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"

	"golang.org/x/term"

	"github.com/tmpfs-code/tmpfs-cli/internal/api"
	"github.com/tmpfs-code/tmpfs-cli/internal/download"
	"github.com/tmpfs-code/tmpfs-cli/internal/progress"
	"github.com/tmpfs-code/tmpfs-cli/internal/tmpcrypto"
	"github.com/tmpfs-code/tmpfs-cli/internal/upload"
)

// version is set at build time with -ldflags "-X main.version=..." from the
// VERSION file. Builds made with "go install module@vX.Y.Z" don't set it, so
// init falls back to the module version recorded by the Go toolchain.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			version = strings.TrimPrefix(v, "v")
		}
	}
}

const (
	defaultServer = "https://tmpfs.it"
	serverEnv     = "TMPFS_SERVER"

	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errUsage marks command-line usage errors (exit code 2).
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

type globals struct {
	server         string
	serverExplicit bool
	quiet          bool
	debug          bool
	stderr         io.Writer
}

const mainUsage = `tmpfs-cli - share files on tmpfs.it with end-to-end encryption

Usage:
  tmpfs-cli [global flags] <command> [flags] <args>

Commands:
  upload,   u   Encrypt and upload a file, print the share link
  download, d   Download and decrypt a file from a share link
  version       Print the version

Global flags:
  --server URL    tmpfs server (default ` + defaultServer + `, env ` + serverEnv + `)
  -q, --quiet     no progress output
  --version       print the version and exit
  -h, --help      show help

Run 'tmpfs-cli <command> -h' for the flags of a command.
`

const uploadUsage = `Usage:
  tmpfs-cli upload [-t DURATION] [-p PROMO_CODE] FILE

Encrypts FILE and uploads it. Prints the share link on stdout.

Flags:
  -t, --duration DURATION   how long the link stays valid (default ` + upload.DefaultDuration + `)
                            one of: %s
  -p, --promo CODE          promo code, needed for files larger than 200 MB
                            or durations longer than 1d
`

const downloadUsage = `Usage:
  tmpfs-cli download [-o PATH] [-f] LINK

Downloads and decrypts the file shared with LINK. Note: every download uses
up one of the downloads allowed for the link.

Flags:
  -o, --output PATH   where to save the file (default: original name in the
                      current directory); if PATH is a directory, the file is
                      saved there with its original name
  -f, --force         overwrite an existing file
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	g := &globals{stderr: stderr}

	err := dispatch(ctx, g, args, stdout)
	if err == nil {
		return exitOK
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}

	var ue errUsage
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "tmpfs-cli: %s\n", ue.msg)
		return exitUsage
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		fmt.Fprintln(stderr, "tmpfs-cli: interrupted")
		return exitError
	}
	if g.debug {
		fmt.Fprintf(stderr, "tmpfs-cli: %+v\n", err)
	} else {
		fmt.Fprintf(stderr, "tmpfs-cli: %s\n", err)
	}
	return exitError
}

func dispatch(ctx context.Context, g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("tmpfs-cli", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&g.server, "server", "", "")
	fs.BoolVar(&g.quiet, "q", false, "")
	fs.BoolVar(&g.quiet, "quiet", false, "")
	fs.BoolVar(&g.debug, "debug", false, "") // hidden
	showVersion := fs.Bool("version", false, "")
	help := fs.Bool("h", false, "")
	fs.BoolVar(help, "help", false, "")

	// global flags come before the command
	if err := fs.Parse(args); err != nil {
		return errUsage{err.Error() + " (see tmpfs-cli -h)"}
	}

	if *showVersion {
		return printVersion(stdout)
	}
	if *help || fs.NArg() == 0 {
		fmt.Fprint(g.stderr, mainUsage)
		if *help {
			return nil
		}
		return errUsage{"missing command"}
	}

	if g.server != "" {
		g.serverExplicit = true
	} else if env := os.Getenv(serverEnv); env != "" {
		g.server, g.serverExplicit = env, true
	} else {
		g.server = defaultServer
	}

	cmd, rest := fs.Arg(0), fs.Args()[1:]
	switch cmd {
	case "upload", "u":
		return cmdUpload(ctx, g, rest, stdout)
	case "download", "d":
		return cmdDownload(ctx, g, rest, stdout)
	case "version":
		if len(rest) > 0 {
			return errUsage{"version takes no arguments"}
		}
		return printVersion(stdout)
	default:
		return errUsage{fmt.Sprintf("unknown command %q (see tmpfs-cli -h)", cmd)}
	}
}

func printVersion(stdout io.Writer) error {
	fmt.Fprintf(stdout, "tmpfs-cli %s\n", version)
	return nil
}

func cmdUpload(ctx context.Context, g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("t", upload.DefaultDuration, "")
	fs.StringVar(duration, "duration", upload.DefaultDuration, "")
	promo := fs.String("p", "", "")
	fs.StringVar(promo, "promo", "", "")

	if err := parseInterspersed(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(g.stderr, uploadUsage, upload.DurationNames())
			return err
		}
		return errUsage{err.Error() + " (see tmpfs-cli upload -h)"}
	}
	if fs.NArg() != 1 {
		return errUsage{"upload needs exactly one FILE (see tmpfs-cli upload -h)"}
	}
	lifetime, err := upload.ParseDuration(*duration)
	if err != nil {
		return errUsage{err.Error()}
	}

	client, err := g.client(g.server)
	if err != nil {
		return err
	}

	link, err := upload.Run(ctx, upload.Options{
		Client:      client,
		Path:        fs.Arg(0),
		LifetimeSec: lifetime,
		PromoCode:   strings.TrimSpace(*promo),
		Warnf:       g.warnf,
		NewProgress: g.newProgress,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, link)
	return nil
}

func cmdDownload(ctx context.Context, g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("o", "", "")
	fs.StringVar(output, "output", "", "")
	force := fs.Bool("f", false, "")
	fs.BoolVar(force, "force", false, "")

	if err := parseInterspersed(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(g.stderr, downloadUsage)
			return err
		}
		return errUsage{err.Error() + " (see tmpfs-cli download -h)"}
	}
	if fs.NArg() != 1 {
		return errUsage{"download needs exactly one LINK (see tmpfs-cli download -h)"}
	}

	link, err := tmpcrypto.ParseLink(fs.Arg(0))
	if err != nil {
		return errUsage{err.Error()}
	}

	// the link's own server, unless one was chosen explicitly
	server := link.Server
	if g.serverExplicit {
		server = g.server
	}
	client, err := g.client(server)
	if err != nil {
		return err
	}

	path, err := download.Run(ctx, download.Options{
		Client:      client,
		Link:        link,
		Output:      *output,
		Force:       *force,
		Infof:       g.infof,
		NewProgress: g.newProgress,
	})
	if err != nil {
		return err
	}
	if !g.quiet {
		fmt.Fprintf(g.stderr, "saved to %s\n", path)
	}
	return nil
}

func (g *globals) client(server string) (*api.Client, error) {
	ua := fmt.Sprintf("tmpfs-cli/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
	c, err := api.New(server, ua)
	if err != nil {
		return nil, errUsage{err.Error()}
	}
	if g.debug {
		c.Debugf = func(format string, args ...any) {
			fmt.Fprintf(g.stderr, "debug: "+format+"\n", args...)
		}
	}
	return c, nil
}

func (g *globals) warnf(format string, args ...any) {
	fmt.Fprintf(g.stderr, "warning: "+format+"\n", args...)
}

func (g *globals) infof(format string, args ...any) {
	if !g.quiet {
		fmt.Fprintf(g.stderr, format+"\n", args...)
	}
}

func (g *globals) newProgress(label string, total int64) *progress.Bar {
	return progress.New(g.stderr, !g.quiet && isTerminal(g.stderr), label, total)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// parseInterspersed parses flags that may appear before or after positional
// arguments (e.g. "upload FILE -t 3d"). "--" ends the flags.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)

		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // fs.Parse reports it
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	if err := fs.Parse(flags); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		positional = append(fs.Args(), positional...)
	}
	return fs.Parse(append([]string{"--"}, positional...))
}
