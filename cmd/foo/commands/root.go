package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"

	"charm.land/log/v2"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"hop.top/foo/internal/config"
	"hop.top/foo/internal/llm"
	"hop.top/foo/internal/pattern"
	"hop.top/foo/internal/schema"
	"hop.top/foo/internal/strategy"
	"hop.top/foo/internal/suggest"
	"hop.top/foo/internal/tool"
	"hop.top/foo/internal/tool/builtin"
	"hop.top/foo/internal/ui"
	"hop.top/foo/internal/workspace"
	extdiscover "hop.top/kit/go/ai/ext/discover"
	extdispatch "hop.top/kit/go/ai/ext/dispatch"
	kitllm "hop.top/kit/go/ai/llm"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
	kitlog "hop.top/kit/go/console/log"
	"hop.top/kit/go/core/upgrade"
	"hop.top/kit/go/core/xdg"
	kitbus "hop.top/kit/go/runtime/bus"
	wsm "hop.top/wsm/pkg/workspace"
)

const longDescription = `foo is an opinionated terminal-first LLM workflow tool.

Use the root command for one-shot prompts, or use grouped subcommands to
manage reusable prompt assets, embeddings, schemas, and interactive sessions.`

var (
	patternName     string
	strategyName    string
	modelName       string
	noStream        bool
	dryRun          bool
	toolNames       []string
	chainLimit      int
	toolsDebug      bool
	toolsApprove    bool
	fragments       []string
	systemFragments []string
	schemaName      string
	schemaMulti     string

	cfg     = config.Default()
	root    *kitcli.Root
	logger  *log.Logger
	eventBus kitbus.Bus
	mgr     *wsm.Manager
	ws      *wsm.Workspace
)

var commandGroups = map[string]string{
	"repl":     "interact",
	"pattern":  "knowledge",
	"strategy": "knowledge",
	"fragment": "knowledge",
	"schema":   "knowledge",
	"embed":    "knowledge",
	"model":    "organize",
	"provider": "organize",
	"upgrade":  "management",
}

// version is bound by main via ldflags (-X main.version) and threaded
// in through New. Defaults to "dev" when unset (go run, go build with
// no ldflags). All paths that report a foo version — --version, the
// foo_version LLM tool, and the upgrade check — read from this single
// value.
var version = "dev"

func New(v string) *kitcli.Root {
	if v != "" {
		version = v
		builtin.Version = v
	}
	root = kitcli.New(kitcli.Config{
		Name:    "foo",
		Version: version,
		Short:   "LLM workflows from the terminal",
		Accent:  config.DefaultAccent,
		Help: kitcli.HelpConfig{
			Disclaimer: longDescription,
			Groups: []kitcli.GroupConfig{
				{ID: "knowledge", Title: "KNOWLEDGE"},
				{ID: "organize", Title: "ORGANIZE"},
				{ID: "interact", Title: "INTERACT"},
			},
		},
	}, kitcli.WithStatus(kitcli.StatusConfig{}))
	logger = kitlog.New(root.Viper)
	slog.SetDefault(slog.New(logger))

	root.Cmd.Use = "foo [prompt]"
	root.Cmd.SilenceUsage = true
	root.Cmd.SilenceErrors = true
	root.Cmd.SuggestionsMinimumDistance = 2
	root.Cmd.Args = cobra.MaximumNArgs(1)
	root.Cmd.PersistentPreRunE = initializeRuntime
	root.Cmd.RunE = runPromptOrREPL

	flags := root.Cmd.Flags()
	flags.StringVarP(&patternName, "pattern", "p", "", "Pattern to apply")
	flags.StringVarP(&strategyName, "strategy", "s", "", "Strategy to wrap the system prompt")
	flags.StringVarP(&modelName, "model", "m", "", "Model override")
	flags.BoolVar(&noStream, "no-stream", false, "Disable streaming output")
	flags.BoolVar(&dryRun, "dry-run", false, "Print assembled prompt without calling the model")
	flags.StringSliceVarP(&toolNames, "tool", "T", nil, "Enable specific tools by name")
	flags.IntVar(&chainLimit, "chain-limit", 5, "Maximum tool-call iterations")
	flags.BoolVar(&toolsDebug, "tools-debug", false, "Write tool call traces to stderr")
	flags.BoolVar(&toolsApprove, "tools-approve", false, "Prompt before each tool execution")
	flags.StringSliceVarP(&fragments, "fragment", "f", nil, "Attach fragment(s) to the user prompt")
	flags.StringSliceVar(&systemFragments, "system-fragment", nil, "Attach fragment(s) to the system prompt")
	flags.StringVar(&schemaName, "schema", "", "Structured JSON output (schema name or DSL)")
	flags.StringVar(&schemaMulti, "schema-multi", "", "Structured JSON array output (schema name or DSL)")

	root.Cmd.AddCommand(replCmd())
	root.Cmd.AddCommand(patternCmd())
	root.Cmd.AddCommand(strategyCmd())
	root.Cmd.AddCommand(fragmentCmd())
	root.Cmd.AddCommand(embedRootCmd())
	root.Cmd.AddCommand(schemaCmd())
	root.Cmd.AddCommand(modelCmd())
	root.Cmd.AddCommand(providerCmd())
	root.Cmd.AddCommand(upgradeCmd())

	registerExtPlugins(root.Cmd)
	applyCommandGroups()
	return root
}

// registerExtPlugins discovers `foo-*` binaries on $PATH, registers each
// as a passthrough subcommand via kit's ext/dispatch helper, and stamps
// the cobra metadata the strict validator demands (Long, side-effect,
// idempotency). Long is sourced from each plugin's --ext-info
// description; on failure we synthesize a non-empty placeholder so the
// validator gate stays armed.
func registerExtPlugins(rootCmd *cobra.Command) {
	before := commandSet(rootCmd)
	extdispatch.Register(rootCmd, "foo", "")
	for _, sub := range rootCmd.Commands() {
		if _, existed := before[sub.Name()]; existed {
			continue
		}
		annotateExtPlugin(sub)
	}
}

func commandSet(c *cobra.Command) map[string]struct{} {
	s := make(map[string]struct{}, len(c.Commands()))
	for _, sub := range c.Commands() {
		s[sub.Name()] = struct{}{}
	}
	return s
}

func annotateExtPlugin(sub *cobra.Command) {
	desc := externalPluginDescription(sub.Name())
	if desc == "" {
		desc = "External plugin discovered on $PATH; see `" + sub.Name() + " --help` for plugin-specific flags."
	}
	if sub.Short == "" {
		sub.Short = desc
	}
	sub.Long = desc
	// External binaries are opaque: side-effect class is unknown, so
	// pick the most conservative classification kit offers — interactive
	// (forces TTY policy off destructive shortcuts) and non-idempotent.
	kitcli.SetSideEffect(sub, kitcli.SideEffectInteractive)
	kitcli.SetIdempotency(sub, kitcli.IdempotencyNo)
	kitcli.SetTopLevelVerb(sub)
	kitcli.SetPassthrough(sub)
}

func externalPluginDescription(name string) string {
	binary := "foo-" + name
	path, err := exec.LookPath(binary)
	if err != nil {
		return ""
	}
	found := extdiscover.Found{Name: name, Path: path}
	if err := found.Enrich(); err != nil {
		return ""
	}
	return found.Meta().Description
}

func initializeRuntime(cmd *cobra.Command, _ []string) error {
	extraPaths, overrides, err := root.ConfigArgs()
	if err != nil {
		return fmt.Errorf("parse config args: %w", err)
	}
	loaded, err := config.Load(config.LoadOptions{
		ExtraConfigPaths: extraPaths,
		Overrides:        overrides,
	})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg = loaded

	verbose, _ := cmd.Root().PersistentFlags().GetCount("verbose")
	logger = kitlog.WithVerbose(root.Viper, verbose)
	slog.SetDefault(slog.New(logger))

	if cmd.Name() != "upgrade" {
		upgrade.NotifyIfAvailable(cmd.Context(), newUpgradeChecker(), cmd.ErrOrStderr())
	}
	if eventBus == nil {
		eventBus = kitbus.New()
	}

	if cmd.CommandPath() == "foo" || cmd.CommandPath() == "foo repl" {
		mgr, ws, err = workspace.InitWorkspace(cmd.Context())
		if err != nil {
			return fmt.Errorf("initialize workspace: %w", err)
		}
	}

	return nil
}

func runPromptOrREPL(cmd *cobra.Command, args []string) error {
	prompt, err := readPrompt(cmd, args)
	if err != nil {
		return err
	}
	if prompt == "" {
		if stdinIsPipe(cmd) {
			return fmt.Errorf("no prompt provided (stdin was empty); pass a positional prompt or pipe non-empty content")
		}
		return runREPL(cmd)
	}

	systemPrompt, err := assembleSystemPrompt(cmd.Context())
	if err != nil {
		return err
	}

	if len(fragments) > 0 {
		fragmentManager, err := newFragmentManager()
		if err != nil {
			return err
		}
		resolved, err := fragmentManager.ResolveMultiple(cmd.Context(), fragments)
		if err != nil {
			return err
		}
		prompt = strings.TrimSpace(prompt + "\n---\n" + resolved)
	}

	if dryRun {
		if systemPrompt != "" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "--- system ---")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), systemPrompt)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "--- user ---")
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), prompt)
		return nil
	}

	client, err := llm.NewClient(cmd.Context(), selectedModel())
	if err != nil {
		return err
	}

	fullPrompt := prompt
	if systemPrompt != "" {
		fullPrompt = systemPrompt + "\n\nUser: " + prompt
	}

	recordMessage(cmd.Context(), "user", prompt)

	if len(toolNames) > 0 {
		registry, err := buildRegistry(toolNames)
		if err != nil {
			return err
		}
		dispatcher := tool.NewDispatcher(client, registry, tool.DispatchConfig{
			ChainLimit:  chainLimit,
			Debug:       toolsDebug,
			DebugWriter: cmd.ErrOrStderr(),
			Approve:     approvalFunc(cmd),
		})
		resp, err := dispatcher.Run(cmd.Context(), fullPrompt)
		if err != nil {
			return err
		}
		recordMessage(cmd.Context(), "assistant", resp)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), resp)
		return nil
	}

	if noStream {
		resp, err := client.Prompt(cmd.Context(), fullPrompt)
		if err != nil {
			return err
		}
		recordMessage(cmd.Context(), "assistant", resp)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), resp)
		return nil
	}

	var buf strings.Builder
	if err := client.PromptStream(cmd.Context(), io.MultiWriter(cmd.OutOrStdout(), &buf), fullPrompt); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout())
	recordMessage(cmd.Context(), "assistant", buf.String())
	return nil
}

func replCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Open an interactive REPL session",
		Long: `Open a multi-turn REPL against the currently selected model
(set via --model, foo model default, or config). Requires a TTY.

Keys: Enter sends the prompt; Ctrl+C or Esc exits.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runREPL(cmd)
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectInteractive)
	kitcli.SetIdempotency(cmd, kitcli.IdempotencyNo)
	kitcli.SetTopLevelVerb(cmd)
	return cmd
}

// stdinIsPipe reports whether stdin is a file descriptor that is not a
// terminal — i.e., a pipe, redirect, or closed handle. The bare command
// uses this to distinguish "no prompt on an interactive TTY (open the
// REPL)" from "no prompt and a piped stdin produced nothing (error)".
func stdinIsPipe(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return true
	}
	return !term.IsTerminal(int(f.Fd()))
}

func runREPL(cmd *cobra.Command) error {
	if f, ok := cmd.InOrStdin().(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		return fmt.Errorf("interactive REPL requires a terminal; supply a prompt (`foo \"...\"`) or pipe input (`echo ... | foo -p <pattern>`)")
	}
	client, err := llm.NewClient(cmd.Context(), selectedModel())
	if err != nil {
		return err
	}
	program := tea.NewProgram(ui.NewREPLModel(cmd.Context(), client))
	_, err = program.Run()
	return err
}

func patternCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pattern",
		Short: "Manage reusable system prompt patterns",
		Long: `Store and retrieve named system-prompt patterns.

Patterns are user-scoped, plain-text system prompts that can be
applied to any prompt via --pattern.`,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available patterns",
		Long:  "List every pattern name in the local pattern directory.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			names, err := pattern.List(cfg.PatternsPath)
			if err != nil {
				return err
			}
			rows := make([]patternRow, 0, len(names))
			for _, name := range names {
				rows = append(rows, patternRow{Name: name})
			}
			return renderData(cmd, rows)
		},
	}
	kitcli.SetSideEffect(listCmd, kitcli.SideEffectRead)
	cmd.AddCommand(listCmd)

	showCmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one pattern",
		Long:  "Print the system-prompt body of one named pattern.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := pattern.LoadPattern(cfg.PatternsPath, args[0])
			if err != nil {
				return enrichPatternNotFound(args[0], err)
			}
			return renderData(cmd, patternView{Name: item.Name, System: item.System})
		},
	}
	kitcli.SetSideEffect(showCmd, kitcli.SideEffectRead)
	cmd.AddCommand(showCmd)

	createCmd := &cobra.Command{
		Use:   "create <name> [system-prompt]",
		Short: "Create or replace a pattern",
		Long:  "Persist a named pattern. When the system-prompt argument is omitted an empty pattern is created.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			systemPrompt := ""
			if len(args) == 2 {
				systemPrompt = args[1]
			}
			if err := pattern.Create(cfg.PatternsPath, args[0], systemPrompt); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pattern %q saved\n", args[0])
			return nil
		},
	}
	kitcli.SetSideEffect(createCmd, kitcli.SideEffectWriteLocal)
	cmd.AddCommand(createCmd)

	importCmd := &cobra.Command{
		Use:   "import <path> [name]",
		Short: "Import a pattern from a file",
		Long:  "Read a pattern definition from a file on disk and store it under the given name (default: file basename).",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			if err := pattern.Import(cfg.PatternsPath, args[0], name); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "pattern imported")
			return nil
		},
	}
	kitcli.SetSideEffect(importCmd, kitcli.SideEffectWriteLocal)
	kitcli.SetIdempotency(importCmd, kitcli.IdempotencyConditional)
	cmd.AddCommand(importCmd)

	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a pattern",
		Long:  "Remove a named pattern from the local pattern directory. Local irreversible.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pattern.Delete(cfg.PatternsPath, args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pattern %q deleted\n", args[0])
			return nil
		},
	}
	kitcli.SetSideEffect(deleteCmd, kitcli.SideEffectDestructiveLocal)
	cmd.AddCommand(deleteCmd)

	return cmd
}

func strategyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "strategy",
		Short: "List available prompt strategies",
		Long: `Strategies are built-in wrappers that decorate a system prompt
(e.g. chain-of-thought, ReAct, scratchpad). Apply one with --strategy.`,
	}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available strategies",
		Long:  "List every built-in prompt strategy with its description.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := strategy.NewManager()
			if err != nil {
				return err
			}
			loaded := manager.List()
			rows := make([]strategyRow, 0, len(loaded))
			for _, item := range loaded {
				rows = append(rows, strategyRow{Name: item.Name, Description: item.Description})
			}
			return renderData(cmd, rows)
		},
	}
	kitcli.SetSideEffect(listCmd, kitcli.SideEffectRead)
	cmd.AddCommand(listCmd)
	return cmd
}

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage the default model selection",
		Long: `Inspect and set the default LLM model used when --model is not
supplied on the command line.`,
	}

	currentCmd := &cobra.Command{
		Use:   "current",
		Short: "Show the current default model",
		Long:  "Print the model id stored in user config as the default.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return renderData(cmd, modelStatus{Current: cfg.Model})
		},
	}
	kitcli.SetSideEffect(currentCmd, kitcli.SideEffectRead)
	cmd.AddCommand(currentCmd)

	defaultCmd := &cobra.Command{
		Use:   "default <model>",
		Short: "Set the default model",
		Long:  "Persist the supplied model id as the new default in user config.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.Model = args[0]
			if err := cfg.Save(); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "default model set to %q\n", cfg.Model)
			return nil
		},
	}
	kitcli.SetSideEffect(defaultCmd, kitcli.SideEffectWriteLocal)
	cmd.AddCommand(defaultCmd)

	return cmd
}

func providerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Inspect configured LLM providers",
		Long: `Inspect which LLM providers are compiled in and whether the
credentials they expect are present in the configured secret store.`,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered providers",
		Long:  "List every LLM provider scheme registered in this build.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			schemes := kitllm.Schemes()
			sort.Strings(schemes)
			rows := make([]providerRow, 0, len(schemes))
			for _, scheme := range schemes {
				rows = append(rows, providerRow{Scheme: scheme})
			}
			return renderData(cmd, rows)
		},
	}
	kitcli.SetSideEffect(listCmd, kitcli.SideEffectRead)
	cmd.AddCommand(listCmd)

	showCmd := &cobra.Command{
		Use:   "show <scheme>",
		Short: "Show provider auth status",
		Long:  "Show the secret key a provider expects and whether it is currently configured.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scheme := strings.TrimSuffix(args[0], "://")
			keyName, authType := providerAuthRequirement(scheme)
			status := providerStatus{
				Scheme:   scheme,
				AuthType: authType,
			}
			if keyName == "" {
				status.Status = "available"
				return renderData(cmd, status)
			}
			status.SecretKey = keyName
			_, ok, err := cfg.LookupSecret(cmd.Context(), keyName)
			if err != nil {
				return err
			}
			if ok {
				status.Status = "configured"
			} else {
				status.Status = "missing"
			}
			return renderData(cmd, status)
		},
	}
	kitcli.SetSideEffect(showCmd, kitcli.SideEffectRead)
	cmd.AddCommand(showCmd)

	return cmd
}

func upgradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade foo to the latest version",
		Long: `Check the GitHub releases for a newer version of foo and apply
the upgrade in-place when one is available. Local binary mutation.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return upgrade.RunCLI(cmd.Context(), newUpgradeChecker(), upgrade.CLIOptions{})
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectWriteLocal)
	kitcli.SetIdempotency(cmd, kitcli.IdempotencyYes)
	kitcli.SetTopLevelVerb(cmd)
	return cmd
}

func buildRegistry(names []string) (*tool.Registry, error) {
	registry := tool.NewRegistry()
	_ = registry.Register(builtin.TimeTool{})
	_ = registry.Register(builtin.VersionTool{})

	scanner := &extdiscover.Scanner{Prefix: "foo-tool-"}
	found, err := scanner.Scan()
	if err != nil {
		return nil, err
	}
	for _, item := range found {
		toolDef := tool.NewExternalTool(item.Name, item.Name, item.Path, nil)
		if err := item.Enrich(); err == nil {
			meta := item.Meta()
			toolDef = tool.NewExternalTool(meta.Name, meta.Description, item.Path, nil)
		}
		_ = registry.Register(toolDef)
	}

	if len(names) > 0 {
		return registry.Filter(names), nil
	}
	return registry, nil
}

func approvalFunc(cmd *cobra.Command) tool.ApproveFunc {
	if !toolsApprove {
		return nil
	}
	return approveFromStdin(cmd.InOrStdin(), cmd.ErrOrStderr())
}

func approveFromStdin(r io.Reader, w io.Writer) tool.ApproveFunc {
	return func(name string, args json.RawMessage) bool {
		_, _ = fmt.Fprintf(w, "[tool] execute %s with %s? [y/N] ", name, string(args))
		scanner := bufio.NewScanner(r)
		if scanner.Scan() {
			answer := strings.TrimSpace(scanner.Text())
			return answer == "y" || answer == "Y"
		}
		return false
	}
}

func assembleSystemPrompt(ctx context.Context) (string, error) {
	systemPrompt := ""
	if patternName != "" {
		loaded, err := pattern.LoadPattern(cfg.PatternsPath, patternName)
		if err != nil {
			return "", enrichPatternNotFound(patternName, err)
		}
		systemPrompt = loaded.System
	}
	if strategyName != "" {
		manager, err := strategy.NewManager()
		if err != nil {
			return "", err
		}
		systemPrompt, err = manager.WrapPrompt(strategyName, systemPrompt)
		if err != nil {
			return "", err
		}
	}
	if len(systemFragments) > 0 {
		fragmentManager, err := newFragmentManager()
		if err != nil {
			return "", err
		}
		resolved, err := fragmentManager.ResolveMultiple(ctx, systemFragments)
		if err != nil {
			return "", err
		}
		systemPrompt = strings.TrimSpace(systemPrompt + "\n---\n" + resolved)
	}
	if schemaName != "" || schemaMulti != "" {
		return appendSchemaPrompt(systemPrompt, schemaName, schemaMulti)
	}
	return strings.TrimSpace(systemPrompt), nil
}

func appendSchemaPrompt(systemPrompt, single, multi string) (string, error) {
	target := single
	arrayMode := false
	if target == "" {
		target = multi
		arrayMode = true
	}
	if target == "" {
		return systemPrompt, nil
	}

	store, err := openSchemaStore()
	if err != nil {
		return "", fmt.Errorf("open schema store: %w", err)
	}

	var schemaJSON string
	stored, err := store.Get(target)
	if err == nil {
		compiled, marshalErr := json.MarshalIndent(stored.Schema, "", "  ")
		if marshalErr != nil {
			return "", marshalErr
		}
		schemaJSON = string(compiled)
	} else {
		compiled, compileErr := schema.CompileDSLJSON(target)
		if compileErr != nil {
			return "", enrichSchemaNotFound(target, fmt.Errorf("schema %q not found and not valid DSL: %w", target, compileErr))
		}
		schemaJSON = compiled
	}

	instruction := "\n\nRespond with valid JSON matching this schema:\n" + schemaJSON
	if arrayMode {
		instruction = "\n\nRespond with a JSON array where each element matches this schema:\n" + schemaJSON
	}
	return strings.TrimSpace(systemPrompt + instruction), nil
}

func readPrompt(cmd *cobra.Command, args []string) (string, error) {
	var stdinText string
	if file, ok := cmd.InOrStdin().(*os.File); ok && !term.IsTerminal(int(file.Fd())) {
		data, err := io.ReadAll(file)
		if err != nil {
			return "", err
		}
		stdinText = strings.TrimSpace(string(data))
	}

	switch {
	case stdinText != "" && len(args) > 0:
		return strings.TrimSpace(args[0] + "\n\n" + stdinText), nil
	case stdinText != "":
		return stdinText, nil
	case len(args) > 0:
		return strings.TrimSpace(args[0]), nil
	default:
		return "", nil
	}
}

func selectedModel() string {
	if modelName != "" {
		return modelName
	}
	return cfg.Model
}

func renderData(cmd *cobra.Command, data any) error {
	return output.Dispatch(cmd, root.Viper, data)
}

func applyCommandGroups() {
	for _, cmd := range root.Cmd.Commands() {
		if groupID, ok := commandGroups[cmd.Name()]; ok {
			cmd.GroupID = groupID
		}
	}
}

func publishEvent(ctx context.Context, topic string, payload any) {
	if eventBus == nil {
		return
	}
	_ = eventBus.Publish(ctx, kitbus.NewEvent(kitbus.Topic(topic), "foo", payload))
}

func recordMessage(ctx context.Context, role, content string) {
	if mgr == nil || ws == nil || content == "" {
		return
	}
	_, _ = mgr.RecordEvent(ctx, ws.ID, wsm.EventInteractionMessage, wsm.MessageData{
		Role:    role,
		Content: content,
	})
}

func newUpgradeChecker() *upgrade.Checker {
	stateDir, err := xdg.StateDir("foo")
	if err != nil {
		stateDir = ""
	}
	return upgrade.New(
		upgrade.WithBinary("foo", version),
		upgrade.WithGitHub("hop-top/foo"),
		upgrade.WithStateDir(stateDir),
	)
}

func providerAuthRequirement(scheme string) (key string, authType string) {
	switch scheme {
	case "anthropic":
		return "anthropic_api_key", "api_key"
	case "openai":
		return "openai_api_key", "api_key"
	case "google", "gemini":
		return "google_api_key", "api_key"
	case "ollama":
		return "", "local"
	default:
		return "", "unknown"
	}
}

// enrichPatternNotFound wraps a "pattern not found" error with an
// actionable hint: closest-matching name (Levenshtein), `pattern list`
// pointer, and `pattern import` syntax for promoting project-local
// patterns. Falls back to the original error if listing fails — the
// hint is best-effort, never blocking.
func enrichPatternNotFound(want string, orig error) error {
	names, listErr := pattern.List(cfg.PatternsPath)
	if listErr != nil || len(names) == 0 {
		return fmt.Errorf("%w; run `foo pattern list` to see available patterns, or `foo pattern import <path> %s` to promote a project-local pattern", orig, want)
	}
	if guess := suggest.Closest(want, names, 2); guess != "" {
		return fmt.Errorf("%w; did you mean %q? (run `foo pattern list` to see all)", orig, guess)
	}
	return fmt.Errorf("%w; available patterns: %s (run `foo pattern import <path> %s` to add a project-local pattern globally)", orig, strings.Join(names, ", "), want)
}

// enrichSchemaNotFound wraps the schema lookup failure with a closest
// suggestion, falling back to a list of available schemas. Errors from
// listing are silent; the hint is best-effort.
func enrichSchemaNotFound(want string, orig error) error {
	store, err := openSchemaStore()
	if err != nil {
		return orig
	}
	names, listErr := store.List()
	if listErr != nil || len(names) == 0 {
		return fmt.Errorf("%w; run `foo schema list` to see stored schemas, or supply a valid DSL string like \"name, age int\"", orig)
	}
	if guess := suggest.Closest(want, names, 2); guess != "" {
		return fmt.Errorf("%w; did you mean %q? (run `foo schema list` to see all)", orig, guess)
	}
	return fmt.Errorf("%w; available schemas: %s", orig, strings.Join(names, ", "))
}

type patternRow struct {
	Name string `json:"name" yaml:"name" table:"NAME,priority=9"`
}

type patternView struct {
	Name   string `json:"name" yaml:"name" table:"NAME,priority=9"`
	System string `json:"system" yaml:"system"`
}

type strategyRow struct {
	Name        string `json:"name" yaml:"name" table:"NAME,priority=9"`
	Description string `json:"description" yaml:"description" table:"DESCRIPTION,priority=8"`
}

type modelStatus struct {
	Current string `json:"current" yaml:"current" table:"CURRENT,priority=9"`
}

type providerRow struct {
	Scheme string `json:"scheme" yaml:"scheme" table:"SCHEME,priority=9"`
}

type providerStatus struct {
	Scheme    string `json:"scheme" yaml:"scheme" table:"SCHEME,priority=9"`
	AuthType  string `json:"auth_type" yaml:"auth_type" table:"AUTH,priority=7"`
	SecretKey string `json:"secret_key,omitempty" yaml:"secret_key,omitempty"`
	Status    string `json:"status" yaml:"status" table:"STATUS,priority=8"`
}
