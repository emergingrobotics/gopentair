// Command pentair is a CLI for the Pentair Home cloud API.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/emergingrobotics/gopentair"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Exit codes.
const (
	exitOK           = 0
	exitError        = 1
	exitUsage        = 2
	exitAuth         = 3
	exitOffline      = 4
	exitNotConfirmed = 5
	exitRateLimited  = 6
)

type globalFlags struct {
	device      string
	json        bool
	csv         bool
	dryRun      bool
	yes         bool
	noConfirm   bool
	configPath  string
	tokenFile   string
	region      string
	signProfile string
	debug       bool
	units       string
}

// app carries shared state; tests replace extraOpts, stdin and stdout.
type app struct {
	flags     globalFlags
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	isTTY     func() bool
	readPass  func() (string, error)
	extraOpts []gopentair.Option
	cfg       *gopentair.Config
	client    gopentair.Client
}

func newApp() *app {
	return &app{
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		isTTY:    stdinIsTerminal,
		readPass: readPassword,
	}
}

// config loads file + env + flags.
func (a *app) config() (*gopentair.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	path := a.flags.configPath
	if path == "" {
		path = os.Getenv("PENTAIR_CONFIG")
	}
	cfg, err := gopentair.LoadConfigFile(path)
	if err != nil {
		return nil, err
	}
	cfg.Merge(gopentair.LoadConfigFromEnv())
	cfg.Merge(&gopentair.Config{
		TokenFile:   a.flags.tokenFile,
		Region:      a.flags.region,
		SignProfile: a.flags.signProfile,
		Debug:       a.flags.debug,
	})
	a.cfg = cfg
	return cfg, nil
}

// getClient builds the client once.
func (a *app) getClient() (gopentair.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	if cfg.Username == "" {
		return nil, usageErr("no username: set PENTAIR_USERNAME or username in " + gopentair.DefaultConfigPath())
	}
	opts := append([]gopentair.Option{
		gopentair.WithDefaultTokenFile(),
		gopentair.WithConfig(cfg),
		gopentair.WithUserAgent("pentair-cli/" + version),
	}, a.extraOpts...)
	c, err := gopentair.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	a.client = c
	return c, nil
}

func (a *app) applyOpts() []gopentair.ApplyOption {
	var o []gopentair.ApplyOption
	if a.flags.dryRun {
		o = append(o, gopentair.WithDryRun())
	}
	if !a.flags.noConfirm {
		o = append(o, gopentair.WithConfirmDefault())
	}
	return o
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }
func usageErr(msg string) error    { return usageError{msg} }

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pentair",
		Short:         "Command-line client for the Pentair Home cloud",
		Long:          "pentair talks to the (unofficial, reverse-engineered) Pentair Home cloud API used by the\nPentair Home app: IntelliFlo 3 pumps, IntelliConnect controllers, Color Sync lights.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	f := root.PersistentFlags()
	f.StringVarP(&a.flags.device, "device", "d", "", "device ID, alias or nickname (default: configured default or the only matching device)")
	f.BoolVar(&a.flags.json, "json", false, "JSON output")
	f.BoolVar(&a.flags.csv, "csv", false, "CSV output")
	f.BoolVar(&a.flags.dryRun, "dry-run", false, "print write plans without sending them")
	f.BoolVarP(&a.flags.yes, "yes", "y", false, "skip confirmation prompts")
	f.BoolVar(&a.flags.noConfirm, "no-confirm", false, "do not wait for the device to reflect a write")
	f.StringVar(&a.flags.configPath, "config", "", "config file (default "+gopentair.DefaultConfigPath()+")")
	f.StringVar(&a.flags.tokenFile, "token-file", "", `session cache file, or "none" (default `+gopentair.DefaultTokenFile()+")")
	f.StringVar(&a.flags.region, "region", "", "region: us (default) or eu")
	f.StringVar(&a.flags.signProfile, "sign-profile", "", "SigV4 header profile: app (default) or standard")
	f.BoolVar(&a.flags.debug, "debug", false, "log redacted requests/responses to stderr")
	f.StringVar(&a.flags.units, "units", "both", "temperature display: f, c or both")

	root.AddCommand(
		a.loginCmd(), a.logoutCmd(), a.whoamiCmd(),
		a.devicesCmd(), a.statusCmd(), a.fieldsCmd(), a.diffCmd(), a.watchCmd(),
		a.pumpCmd(), a.poolCmd(), a.lightCmd(),
		a.versionCmd(),
	)
	return root
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(a.stdout, "pentair %s (commit %s, built %s, gopentair %s)\n", version, commit, date, gopentair.Version)
		},
	}
}

// exitCode maps an error to a process exit code.
func exitCode(err error) int {
	var ue usageError
	var ce *gopentair.ChallengeError
	var ae *gopentair.AuthError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ue), errors.Is(err, gopentair.ErrInvalidValue), errors.Is(err, gopentair.ErrAmbiguousDevice):
		return exitUsage
	case errors.Is(err, gopentair.ErrReauthRequired), errors.Is(err, gopentair.ErrInvalidCredentials),
		errors.As(err, &ce), errors.As(err, &ae):
		return exitAuth
	case errors.Is(err, gopentair.ErrDeviceOffline):
		return exitOffline
	case errors.Is(err, gopentair.ErrNotAcknowledged), errors.Is(err, gopentair.ErrNotConfirmed):
		return exitNotConfirmed
	case errors.Is(err, gopentair.ErrRateLimited):
		return exitRateLimited
	}
	return exitError
}

func hint(err error) string {
	switch {
	case errors.Is(err, gopentair.ErrReauthRequired):
		return "run: pentair login"
	case errors.Is(err, gopentair.ErrAmbiguousDevice):
		return "pass --device (see: pentair devices list)"
	case errors.Is(err, gopentair.ErrNotConfirmed):
		return "the cloud accepted the write but the device has not reported the change yet"
	}
	return ""
}

func (a *app) run(ctx context.Context, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	if strings.HasPrefix(err.Error(), "unknown command") || strings.HasPrefix(err.Error(), "unknown flag") ||
		strings.Contains(err.Error(), "accepts") || strings.Contains(err.Error(), "required flag") {
		fmt.Fprintln(a.stderr, "error:", err)
		return exitUsage
	}
	fmt.Fprintln(a.stderr, "error:", err)
	if h := hint(err); h != "" {
		fmt.Fprintln(a.stderr, "hint:", h)
	}
	return exitCode(err)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := newApp().run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
