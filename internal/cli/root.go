// Package cli wires the cobra command tree and the process exit codes. It is
// thin: every command resolves its profile and clients through App and then
// calls a service or the Graph layer. All output goes through output.Printer,
// which is what the forbidigo linter enforces.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/auth"
	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Version information, injected at build time with
//
//	-ldflags "-X main.version=… -X main.commit=… -X main.date=…"
//
// (see the build/install tasks in mise.toml and .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// App carries everything a command needs: the streams, the resolved profile and
// lazily built clients. One App per process run keeps tests able to run the CLI
// in-process with their own streams and fakes.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Clock is injectable so relative output and retry math are deterministic.
	Clock clock.Clock
	// Hooks are the in-process test seams (see hooks.go). Production leaves them
	// empty: there is no flag or environment variable that can set them.
	Hooks Hooks

	Printer *output.Printer

	// global flags
	profileFlag  string
	jsonFlag     bool
	noColorFlag  bool
	quietFlag    bool
	verboseFlag  int
	noInputFlag  bool
	readOnlyFlag bool
	jqFlag       string
	refreshFlag  bool

	cfg       *config.Config
	effective *config.Effective
	paths     *store.Paths
	authc     *auth.Client
	graphc    *graph.Client
	graphErr  error

	resolverc   *ref.Resolver
	entityCache *store.EntityCache
	aliases     map[string]string
}

// New builds an App around the given streams. The Printer is built here rather
// than only in Execute so that any App method (confirm, Debugf) is safe to call
// before a command runs; Execute rebuilds it once the global flags are known.
func New(stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		Clock:   clock.New(),
		Printer: output.New(output.Options{Out: stdout, Err: stderr}),
	}
}

// Execute runs the CLI with the given arguments (without the program name).
func (a *App) Execute(ctx context.Context, args []string) error {
	root := a.newRootCmd()
	root.SetArgs(args)
	a.Printer = output.New(output.Options{Out: a.Stdout, Err: a.Stderr})
	return classifyCobraError(root.ExecuteContext(ctx))
}

// classifyCobraError maps cobra's own rejections (an unknown command, a bad
// flag, a wrong argument count) to exit code 2. Cobra returns them as plain
// errors, and the exit-code contract says 2 means "usage".
func classifyCobraError(err error) error {
	if err == nil {
		return nil
	}
	var classified *output.Error
	if errors.As(err, &classified) {
		return err
	}
	// An error from our own layers (a Graph APIError, for example) already knows
	// its exit code; only cobra's own complaints are usage errors.
	var coder output.ExitCoder
	if errors.As(err, &coder) {
		return err
	}
	msg := err.Error()
	// These are cobra's messages verbatim; anything else keeps its own code.
	for _, pattern := range []string{
		"unknown command ",
		"unknown flag: ",
		"unknown shorthand flag: ",
		"flag needs an argument: ",
		"invalid argument ",
		"accepts ",
		"requires at least ",
		"requires exactly ",
		"requires at most ",
		"required flag(s) ",
		"unknown help topic ",
	} {
		if strings.Contains(msg, pattern) {
			return output.WithHint(output.Usagef("%s", msg), "run `teams --help` for the available commands and flags")
		}
	}
	return err
}

// Main is the process entry point: it runs the CLI, prints the error once and
// returns the documented exit code. cmd/teams/main.go calls os.Exit with it.
func Main(args []string) int {
	return MainWith(New(os.Stdin, os.Stdout, os.Stderr), args)
}

// MainWith runs the CLI on a prepared App. main.go uses Main; the in-process
// testscript harness uses MainWith so it can install its fakes without any
// test-only flag or environment variable reaching a release build.
func MainWith(app *App, args []string) int {
	err := app.Execute(context.Background(), args)
	if err != nil {
		if app.Printer == nil {
			// A flag-parsing failure happens before PersistentPreRunE, so the
			// printer may not exist yet.
			app.Printer = output.New(output.Options{Out: app.Stdout, Err: app.Stderr})
		}
		app.Printer.ReportError(err)
		return output.CodeOf(err)
	}
	return output.CodeOK
}

// ConfigPath is the config file location: TEAMS_CONFIG when set, otherwise the
// platform default. It deliberately does not depend on the selected profile, so
// loading the config can never recurse into resolving a profile.
func (a *App) ConfigPath() string {
	if a.Hooks.ConfigPath != "" {
		return a.Hooks.ConfigPath
	}
	if p := os.Getenv(store.EnvConfig); p != "" {
		return p
	}
	paths, err := store.Resolve(config.DefaultProfileName)
	if err != nil {
		return ""
	}
	return paths.ConfigFile
}

// Config returns the loaded config file, loading it on first use.
func (a *App) Config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(a.ConfigPath())
	if err != nil {
		return nil, output.Errorf("%v", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, output.WithHint(output.Usagef("%v", err), "fix the file, or run `teams config list` to see it")
	}
	a.cfg = cfg
	return cfg, nil
}

// Effective resolves the profile with every default filled in.
func (a *App) Effective() (config.Effective, error) {
	if a.effective != nil {
		return *a.effective, nil
	}
	cfg, err := a.Config()
	if err != nil {
		return config.Effective{}, err
	}
	eff, err := cfg.Resolve(config.ResolveInput{
		ProfileFlag:  a.profileFlag,
		ReadOnlyFlag: a.readOnlyFlag,
		Environ:      a.environ(),
	})
	if err != nil {
		return config.Effective{}, output.WithHint(output.Usagef("%v", err), "see `teams profile list` and `teams config list`")
	}
	a.effective = &eff
	return eff, nil
}

// Paths returns the on-disk layout for the selected profile.
func (a *App) Paths() (store.Paths, error) {
	if a.paths != nil {
		return *a.paths, nil
	}
	eff, err := a.Effective()
	if err != nil {
		return store.Paths{}, err
	}
	paths, err := store.Resolve(eff.Name)
	if err != nil {
		return store.Paths{}, output.Usagef("%v", err)
	}
	a.paths = &paths
	return paths, nil
}

// environ returns the environment the auth layer should read, with the test
// hooks applied.
func (a *App) environ() []string {
	env := os.Environ()
	if a.Hooks.Environ != nil {
		env = a.Hooks.Environ
	}
	return env
}

// Auth returns the auth session for the selected profile.
func (a *App) Auth(ctx context.Context) (*auth.Client, error) {
	if a.authc != nil {
		return a.authc, nil
	}
	eff, err := a.Effective()
	if err != nil {
		return nil, err
	}
	paths, err := a.Paths()
	if err != nil {
		return nil, err
	}
	a.Printer.Debugf("auth: profile %s, tenant %s, cloud %s, token store %s",
		eff.Name, eff.Tenant, eff.Cloud, storeKindLabel(eff.TokenStore))
	client, err := auth.New(ctx, auth.Options{
		Effective:                eff,
		Paths:                    paths,
		Clock:                    a.Clock,
		Environ:                  a.environ(),
		HTTPClient:               a.Hooks.AuthHTTPClient,
		DisableInstanceDiscovery: a.Hooks.DisableInstanceDiscovery,
		Authority:                a.Hooks.AuthAuthority,
		OpenURL:                  a.Hooks.AuthOpenURL,
	})
	if err != nil {
		return nil, auth.Classify(err, eff.Name)
	}
	if w := client.Store(); w != nil && w.Warning() != "" {
		a.Printer.Warnf("%s", w.Warning())
	}
	a.authc = client
	return client, nil
}

// Graph returns a Graph client for the selected profile, building it on first
// use. TEAMS_ACCESS_TOKEN and the cached account both flow through auth.Client,
// which is a graph.TokenSource.
func (a *App) Graph(ctx context.Context) (*graph.Client, error) {
	if a.graphc != nil || a.graphErr != nil {
		return a.graphc, a.graphErr
	}
	authClient, err := a.Auth(ctx)
	if err != nil {
		a.graphErr = err
		return nil, err
	}
	eff, err := a.Effective()
	if err != nil {
		a.graphErr = err
		return nil, err
	}
	baseURL := eff.GraphBaseURL
	if a.Hooks.GraphBaseURL != "" {
		baseURL = a.Hooks.GraphBaseURL
	}
	client, err := graph.New(graph.Options{
		BaseURL:    baseURL,
		Token:      authClient,
		HTTPClient: a.Hooks.GraphHTTPClient,
		UserAgent:  "teams-cli/" + Version,
		Timeout:    graph.DefaultTimeout,
		Logger:     a.Printer.Debugf,
		Clock:      a.Clock,
		Sleeper:    a.Hooks.Sleeper,
		Recorder:   a.Hooks.Recorder,
	})
	if err != nil {
		a.graphErr = output.Errorf("%v", err)
		return nil, a.graphErr
	}
	a.graphc = client
	return client, nil
}

// Root returns the command tree, for the doc generator and tests.
func (a *App) Root() *cobra.Command { return a.newRootCmd() }

// newRootCmd builds the command tree for this App.
func (a *App) newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "teams",
		Short: "Search, read and post in Microsoft Teams from the terminal",
		Long: "teams is a standalone CLI for Microsoft Teams.\n\n" +
			"It signs in as you (or as a service account) with delegated Microsoft Graph\n" +
			"permissions, keeps the token cache encrypted, and never prompts in\n" +
			"non-interactive mode.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			interactive := output.DetectInteractive(a.Stdin, a.Stdout, a.noInputFlag)
			a.Printer = output.New(output.Options{
				Out:         a.Stdout,
				Err:         a.Stderr,
				JSON:        a.jsonFlag || a.jqFlag != "",
				JQ:          a.jqFlag,
				NoColor:     a.noColorFlag,
				Quiet:       a.quietFlag,
				Verbose:     a.verboseFlag,
				Interactive: interactive,
			})
			if a.readOnlyFlag || config.ParseBoolEnv(a.environ(), config.EnvReadOnly) {
				if isWriteCommand(cmd) {
					return output.WithHint(output.Usagef("%s is a write command and this session is read-only", cmd.CommandPath()),
						"drop --read-only / TEAMS_READ_ONLY=1, or use a profile with mode = \"full\"")
				}
			}
			return nil
		},
		// A successful command persists what name resolution learned, so the next
		// run resolves a team, channel or person without another scan
		// (PLAN.md:170). A failed command leaves the cache alone.
		PersistentPostRunE: func(*cobra.Command, []string) error {
			a.saveEntityCache()
			return nil
		},
	}
	// Cobra writes help and usage to its own writer by default (os.Stdout), which
	// would bypass the Printer and the tests' buffers.
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)

	// Exit code 2 for anything cobra rejects, and no usage dump on failure.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return output.WithHint(output.Usagef("%v", err), "run `teams --help` for the available flags")
	})

	pf := root.PersistentFlags()
	pf.StringVar(&a.profileFlag, "profile", "", "profile to use (default: the configured default_profile)")
	pf.BoolVar(&a.jsonFlag, "json", false, "machine-readable JSON output")
	pf.BoolVar(&a.noColorFlag, "no-color", false, "disable colours (also honours NO_COLOR)")
	pf.BoolVar(&a.quietFlag, "quiet", false, "suppress progress messages on stderr")
	pf.CountVarP(&a.verboseFlag, "verbose", "v", "verbose diagnostics on stderr (-v, -vv)")
	pf.BoolVar(&a.noInputFlag, "no-input", false, "never prompt; fail fast instead")
	pf.BoolVar(&a.readOnlyFlag, "read-only", false, "refuse write commands")
	pf.StringVar(&a.jqFlag, "jq", "", "filter JSON output through a jq expression (implies --json)")
	pf.BoolVar(&a.refreshFlag, "refresh", false, "ignore cached names and resolve them again")

	root.AddCommand(
		a.newAuthCmd(),
		a.newWhoamiCmd(),
		a.newDoctorCmd(),
		a.newCacheCmd(),
		a.newProfileCmd(),
		a.newConfigCmd(),
		a.newVersionCmd(),
		// Phase 3: the read commands.
		a.newTeamCmd(),
		a.newChannelCmd(),
		a.newChatCmd(),
		a.newThreadCmd(),
		a.newSearchCmd(),
		a.newMentionsCmd(),
		a.newUserCmd(),
		a.newUnreadCmd(),
		a.newFileCmd(),
		a.newAliasCmd(),
	)
	return root
}

// isWriteCommand reports whether a command changes anything in Teams. The list
// grows with Phase 4; it is a safety check that runs before any network call.
func isWriteCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "post", "reply", "edit", "delete", "react":
			return true
		case "login", "logout", "mark-read", "mark-unread", "add-member", "create":
			return true
		}
	}
	return false
}

// requireScope fails with exit 3 before any Graph call when the token does not
// carry a scope a command needs, naming the scope and the preset that has it
// (PLAN.md:66). The TEAMS_ACCESS_TOKEN escape hatch is exempt: that token is not
// scope-checked at all, and PLAN.md says so.
func (a *App) requireScope(ctx context.Context, command string, required []string) error {
	if a.Hooks.GrantedScopes != nil {
		return a.requireScopesFrom(command, a.Hooks.GrantedScopes, required)
	}
	authClient, err := a.Auth(ctx)
	if err != nil {
		return err
	}
	if authClient.Static() {
		a.Printer.Debugf("TEAMS_ACCESS_TOKEN is set; skipping the scope pre-check")
		return nil
	}
	granted, err := authClient.GrantedScopes(ctx)
	if err != nil {
		return err
	}
	return a.requireScopesFrom(command, granted, required)
}

// requireScopesFrom applies the any-of rule: a feature needs one of the listed
// scopes, and if none is present the command stops before any Graph call.
func (a *App) requireScopesFrom(command string, granted, required []string) error {
	missing := config.Scopes.Missing(granted, required)
	if len(missing) < len(required) {
		return nil
	}
	err := config.NewMissingScopeError(command, missing)
	return output.WithHint(output.Authf("%s", err.Error()), err.Hint)
}

// confirm asks a yes/no question on a TTY and refuses in non-interactive mode
// unless the caller passed --yes (PLAN.md: non-interactive mode never prompts).
func (a *App) confirm(prompt string, yes bool) error {
	if yes {
		return nil
	}
	if !a.Printer.Interactive() {
		return output.WithHint(output.Usagef("%s needs confirmation", prompt), "re-run with --yes in non-interactive mode")
	}
	_, _ = fmt.Fprintf(a.Stderr, "%s [y/N] ", prompt)
	var answer string
	if _, err := fmt.Fscanln(a.Stdin, &answer); err != nil {
		return output.Usagef("no answer read; re-run with --yes")
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return output.Errorf("aborted")
	}
}
