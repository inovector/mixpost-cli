// Package cli wires the Mixpost API to cobra commands.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/inovector/mixpost-cli/internal/config"
	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/inovector/mixpost-cli/internal/update"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// skipClientAnnotation marks commands that must run without credentials.
const skipClientAnnotation = "mixpost_skip_client"

type globalFlags struct {
	profile   string
	url       string
	key       string
	workspace string
	format    string
	template  string
	noHeaders bool
	compact   bool
	timeout   time.Duration
	retries   int
	insecure  bool
	debug     bool
	dryRun    bool
	yes       bool
}

// Env carries everything a command needs at run time.
type Env struct {
	Client  *mixpost.Client
	Printer *output.Printer
	Config  *config.File
	Profile string
	DryRun  bool
	Yes     bool
	Debug   bool

	// ctx is the command's context, which Ctrl+C cancels.
	ctx context.Context
}

var (
	flags globalFlags
	env   *Env

	// Streams the CLI reads and writes. Tests replace them.
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	stdin  *os.File  = os.Stdin
)

// NewRootCommand builds the command tree.
func NewRootCommand() *cobra.Command {
	flags = globalFlags{}

	root := &cobra.Command{
		Use:   "mixpost",
		Short: "Control Mixpost from the command line",
		Long: `mixpost drives the Mixpost API from scripts, CI jobs and AI agents.

Every command writes data to stdout and progress or errors to stderr, so output
can be piped safely. Output is a table on a terminal and JSON everywhere else.

Getting started:
  mixpost auth login --personal-key <personal-key>
  mixpost workspace list
  mixpost config set workspace <uuid>
  mixpost account list`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: setupEnv,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flags.profile, "profile", "", "configuration profile to use ($MIXPOST_PROFILE)")
	pf.StringVar(&flags.url, "url", "", "self-hosted Mixpost address, e.g. https://example.com/mixpost ($MIXPOST_URL; default: Mixpost Cloud)")
	pf.StringVar(&flags.key, "personal-key", "", "personal key ($MIXPOST_PERSONAL_KEY)")
	pf.StringVarP(&flags.workspace, "workspace", "w", "", "workspace uuid or name; overrides the profile's workspace")
	pf.StringVarP(&flags.format, "output", "o", "", "output format: "+strings.Join(output.Formats, ", ")+" (default table on a terminal, json otherwise)")
	pf.StringVar(&flags.template, "template", "", "Go template used with --output template")
	pf.BoolVar(&flags.noHeaders, "no-headers", false, "omit the header row in table, csv and tsv output")
	pf.BoolVar(&flags.compact, "compact", false, "print JSON on a single line")
	pf.DurationVar(&flags.timeout, "timeout", 60*time.Second, "timeout for a single HTTP request")
	pf.IntVar(&flags.retries, "retries", 2, "retries for rate-limited, failed or interrupted requests")
	pf.BoolVar(&flags.insecure, "insecure", false, "skip TLS certificate verification")
	pf.BoolVar(&flags.debug, "debug", false, "trace HTTP requests on stderr")
	pf.BoolVar(&flags.dryRun, "dry-run", false, "print the request that would be sent and exit; read-only name lookups still run")
	pf.BoolVarP(&flags.yes, "yes", "y", false, "answer yes to confirmation prompts")

	root.AddCommand(
		newAuthCommand(),
		newConfigCommand(),
		newAccountCommand(),
		newPostCommand(),
		newTagCommand(),
		newMediaCommand(),
		newMentionCommand(),
		newAnalyticsCommand(),
		newWorkspaceCommand(),
		newPingCommand(),
		newRawCommand(),
		newVersionCommand(),
	)

	root.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return output.Formats, cobra.ShellCompDirectiveNoFileComp
	})

	// A malformed invocation should exit with the usage code, not the generic
	// one, so a script can tell "I called this wrong" from "the API said no".
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &usageErr{err: err, command: cmd.CommandPath()}
	})

	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notice := startUpdateCheck(ctx)

	root := NewRootCommand()
	executed, err := root.ExecuteContextC(ctx)
	code := ExitOK
	if err != nil && !isBrokenPipe(err) {
		err = asUsageError(err, commandPath(executed, root))
		renderError(env.printerOrDefault(), err)
		code = ExitCode(err)
	}

	notice.print(executed)
	return code
}

// updateNotice carries a background check for a newer release, so the command
// never waits on GitHub before doing its work.
type updateNotice struct {
	result chan string
}

// updateWait is how long a finished command waits for a check still running.
// A check runs at most once a day, so this delay is rare.
const updateWait = time.Second

// startUpdateCheck starts the daily check when someone is at a terminal to
// read the result. CI, an opt-out, shell completion and output read by another
// program all skip it.
func startUpdateCheck(ctx context.Context) *updateNotice {
	if update.Disabled() != "" || !isTerminal(stderr) || isCompletion(os.Args) {
		return nil
	}
	checker, err := update.New(Version())
	if err != nil {
		return nil
	}

	notice := &updateNotice{result: make(chan string, 1)}
	go func() {
		latest, _ := checker.Newer(ctx)
		notice.result <- latest
	}()
	return notice
}

func (n *updateNotice) print(executed *cobra.Command) {
	if n == nil || env == nil || env.Printer.Format != output.FormatTable {
		return
	}
	if executed != nil && strings.HasPrefix(executed.Name(), "__complete") {
		return
	}

	var latest string
	select {
	case latest = <-n.result:
	case <-time.After(updateWait):
	}
	if latest == "" {
		return
	}

	path, _ := os.Executable()
	fmt.Fprintf(env.Printer.Err, "\nA new version of mixpost is available: %s → %s\nUpdate with: %s\n",
		strings.TrimPrefix(Version(), "v"), latest, update.Command(path))
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && output.IsTTY(f)
}

func isCompletion(args []string) bool {
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "__complete") || arg == "completion" {
			return true
		}
	}
	return false
}

func (e *Env) printerOrDefault() *output.Printer {
	if e != nil && e.Printer != nil {
		return e.Printer
	}
	return output.New(output.DefaultFormat())
}

func setupEnv(cmd *cobra.Command, _ []string) error {
	format := flags.format
	if format == "" {
		format = os.Getenv(config.EnvOutput)
	}

	var parsed output.Format
	if format == "" {
		parsed = output.DefaultFormat()
	} else {
		var err error
		parsed, err = output.ParseFormat(format)
		if err != nil {
			return usageError(err)
		}
	}
	if flags.template != "" && flags.format == "" {
		parsed = output.FormatTemplate
	}

	printer := output.New(parsed)
	printer.Out = stdout
	printer.Err = stderr
	printer.Template = flags.template
	printer.NoHeaders = flags.noHeaders
	printer.Compact = flags.compact

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	profileName := cfg.ProfileName(flags.profile)

	env = &Env{
		Printer: printer,
		Config:  cfg,
		Profile: profileName,
		DryRun:  flags.dryRun,
		Yes:     flags.yes,
		Debug:   flags.debug,
		ctx:     ctxOf(cmd),
	}

	if skipsClient(cmd) {
		return nil
	}

	if flags.profile != "" && cfg.Profile(flags.profile) == nil {
		return usageError(fmt.Errorf("profile %q is not configured (see 'mixpost config list')", flags.profile))
	}

	baseURL, key, workspace := resolveCredentials(cfg, profileName)
	if key == "" {
		return usageError(fmt.Errorf("no personal key configured; run 'mixpost auth login' or set %s", config.EnvKey))
	}

	env.Client = mixpost.New(mixpost.Options{
		BaseURL:   baseURL,
		Key:       key,
		Workspace: workspace,
		Timeout:   flags.timeout,
		Retries:   flags.retries,
		Insecure:  flags.insecure || truthy(os.Getenv(config.EnvInsecure)),
		UserAgent: userAgent(),
		Debug:     flags.debug,
		DebugOut:  printer.Err,
		DryRun:    flags.dryRun,
	})
	return nil
}

// resolveCredentials applies the precedence flag > environment > profile. The
// workspace has no environment step on purpose; see the note in package config.
func resolveCredentials(cfg *config.File, profileName string) (baseURL, key, workspace string) {
	baseURL = config.DefaultBaseURL
	if profile := cfg.Profile(profileName); profile != nil {
		baseURL, key, workspace = profile.BaseURL(), profile.Key, profile.Workspace
	}
	if v := os.Getenv(config.EnvURL); v != "" {
		baseURL = v
	}
	if v := os.Getenv(config.EnvKey); v != "" {
		key = v
	}
	if flags.url != "" {
		baseURL = flags.url
	}
	if flags.key != "" {
		key = flags.key
	}
	if flags.workspace != "" {
		workspace = flags.workspace
	}
	if normalized, err := config.NormalizeBaseURL(baseURL); err == nil {
		baseURL = normalized
	}
	return baseURL, key, workspace
}

func skipsClient(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[skipClientAnnotation] == "true" {
			return true
		}
	}
	return false
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Render prints an API response, honouring --dry-run.
func (e *Env) Render(resp *mixpost.Response, columns []output.Column) error {
	if resp == nil {
		return nil
	}
	if resp.Status == 0 {
		return e.renderDryRun(resp.Request)
	}
	return e.Printer.Print(resp.Body, columns)
}

// RenderWith prints an API response whose tabular records are picked by rows
// rather than read from the "data" collection.
func (e *Env) RenderWith(resp *mixpost.Response, rows func(any) []any, columns []output.Column) error {
	if resp == nil {
		return nil
	}
	if resp.Status == 0 {
		return e.renderDryRun(resp.Request)
	}
	return e.Printer.PrintWith(resp.Body, rows, columns)
}

// RenderRaw prints a payload the command assembled itself.
func (e *Env) RenderRaw(raw []byte, columns []output.Column) error {
	if e.DryRun {
		return e.printJSONBytes(raw)
	}
	return e.Printer.Print(raw, columns)
}

func (e *Env) renderDryRun(info mixpost.RequestInfo) error {
	encoded, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return e.printJSONBytes(encoded)
}

func (e *Env) printJSONBytes(raw []byte) error {
	jsonPrinter := *e.Printer
	jsonPrinter.Format = output.FormatJSON
	return jsonPrinter.Print(raw, nil)
}

// Confirm asks before a destructive action. Without a terminal it refuses
// instead of assuming consent, so an unattended run has to say --yes.
func (e *Env) Confirm(prompt string) error {
	if e.Yes || e.DryRun {
		return nil
	}
	if !output.IsTTY(stdin) {
		return usageError(fmt.Errorf("%s\nthis is not an interactive terminal: pass --yes to confirm", prompt))
	}

	fmt.Fprintf(e.Printer.Err, "%s [y/N]: ", prompt)
	answer, err := readTerminal(e.ctx, e.Printer.Err, false)
	if err != nil {
		return err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return nil
	}
	return errAborted
}

// readTerminal reads one line from the terminal, without echoing it when
// secret. Ctrl+C cancels the command's context instead of killing the process,
// so the read is abandoned as soon as ctx is done, with the terminal restored.
func readTerminal(ctx context.Context, w io.Writer, secret bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fd := int(stdin.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("read from the terminal: %w", err)
	}

	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if secret {
			value, err := term.ReadPassword(fd)
			fmt.Fprintln(w)
			done <- result{string(value), err}
			return
		}
		line, err := bufio.NewReader(stdin).ReadString('\n')
		done <- result{line, err}
	}()

	select {
	case r := <-done:
		if r.err != nil && r.line == "" {
			return "", fmt.Errorf("read from the terminal: %w", r.err)
		}
		return strings.TrimSpace(r.line), nil
	case <-ctx.Done():
		term.Restore(fd, state)
		fmt.Fprintln(w)
		return "", errAborted
	}
}

func commandPath(executed, root *cobra.Command) string {
	if executed != nil {
		return executed.CommandPath()
	}
	return root.CommandPath()
}

func ctxOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
