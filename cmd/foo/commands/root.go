package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/log/v2"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"hop.top/foo/internal/config"
	"hop.top/foo/internal/llm"
	"hop.top/foo/internal/pattern"
	"hop.top/foo/internal/strategy"
	"hop.top/foo/internal/ui"
	"hop.top/foo/internal/workspace"
	"hop.top/kit/cli"
	kitlog "hop.top/kit/log"
	"hop.top/kit/upgrade"
	"hop.top/kit/xdg"
	wsm "hop.top/wsm/pkg/workspace"
)

var (
	patternName string
	modelName   string
	noStream    bool
	dryRun      bool
	cfg         config.Config
	root        *cli.Root
	mgr         *wsm.Manager
	ws          *wsm.Workspace
	logger      *log.Logger
)

func New() *cli.Root {
	var err error
	cfg, err = config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
	}

	root = cli.New(cli.Config{
		Name:    "foo",
		Version: "0.1.0",
		Short:   "foo is an opinionated LLM CLI/REPL",
		Accent:  cfg.Accent,
	})

	logger = kitlog.New(root.Viper)

	root.Cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Check for upgrades
		stateDir, _ := xdg.StateDir("foo")
		checker := upgrade.New(
			upgrade.WithBinary("foo", "0.1.0"),
			upgrade.WithGitHub("hop-top/foo"),
			upgrade.WithStateDir(stateDir),
		)
		upgrade.NotifyIfAvailable(ctx, checker, cmd.ErrOrStderr())

		// Init workspace (WSM)
		var err error
		mgr, ws, err = workspace.InitWorkspace(ctx)
		if err != nil {
			return fmt.Errorf("initialize workspace: %w", err)
		}

		// Record session start
		_, _ = mgr.RecordEvent(ctx, ws.ID, "session.start", map[string]any{
			"command": cmd.CommandPath(),
			"args":    args,
			"cwd":     os.Getenv("PWD"),
		})

		return nil
	}

	root.Cmd.Args = cobra.MaximumNArgs(1)
	root.Cmd.Run = func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		out := cmd.OutOrStdout()

		// Load pattern if specified
		var sysPrompt string
		if patternName != "" {
			p, err := pattern.LoadPattern(cfg.PatternsPath, patternName)
			if err != nil {
				logger.Error("Error loading pattern", "name", patternName, "err", err)
				os.Exit(1)
			}
			sysPrompt = p.System
		}

		mName := modelName
		if mName == "" {
			mName = cfg.Model
		}

		// Read stdin if piped
		var stdinText string
		if f, ok := cmd.InOrStdin().(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
			b, err := io.ReadAll(f)
			if err != nil {
				logger.Error("Error reading stdin", "err", err)
				os.Exit(1)
			}
			stdinText = string(b)
		}

		// Build prompt: stdin + arg are combined
		var prompt string
		switch {
		case stdinText != "" && len(args) > 0:
			prompt = args[0] + "\n\n" + stdinText
		case stdinText != "":
			prompt = stdinText
		case len(args) > 0:
			prompt = args[0]
		}

		if prompt != "" {
			fullPrompt := prompt
			if sysPrompt != "" {
				fullPrompt = fmt.Sprintf("%s\n\nUser: %s", sysPrompt, prompt)
			}

			// --dry-run: print assembled prompt and exit
			if dryRun {
				if sysPrompt != "" {
					fmt.Fprintln(out, "--- system ---")
					fmt.Fprintln(out, sysPrompt)
					fmt.Fprintln(out, "--- user ---")
					fmt.Fprintln(out, prompt)
				} else {
					fmt.Fprintln(out, prompt)
				}
				return
			}

			client, err := llm.NewClient(ctx, mName)
			if err != nil {
				logger.Error("Error creating LLM client", "err", err)
				os.Exit(1)
			}

			_, _ = mgr.RecordEvent(ctx, ws.ID, "interaction.prompt", map[string]any{
				"prompt":  prompt,
				"pattern": patternName,
				"model":   mName,
			})

			if noStream {
				resp, err := client.Prompt(ctx, fullPrompt)
				if err != nil {
					logger.Error("Error from LLM", "err", err)
					os.Exit(1)
				}

				_, _ = mgr.RecordEvent(ctx, ws.ID, "interaction.response", map[string]any{
					"response": resp,
				})

				fmt.Fprintln(out, resp)
			} else {
				var buf strings.Builder
				w := io.MultiWriter(out, &buf)
				if err := client.PromptStream(ctx, w, fullPrompt); err != nil {
					logger.Error("Error from LLM", "err", err)
					os.Exit(1)
				}
				fmt.Fprintln(out)

				_, _ = mgr.RecordEvent(ctx, ws.ID, "interaction.response", map[string]any{
					"response": buf.String(),
				})
			}
		} else {
			// REPL mode — requires interactive terminal
			if f, ok := cmd.InOrStdin().(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
				fmt.Fprintln(cmd.ErrOrStderr(), "error: REPL requires an interactive terminal; provide a prompt or pipe input")
				os.Exit(1)
			}

			client, err := llm.NewClient(ctx, mName)
			if err != nil {
				logger.Error("Error creating LLM client", "err", err)
				os.Exit(1)
			}

			p := tea.NewProgram(ui.NewREPLModel(ctx, client))
			if _, err := p.Run(); err != nil {
				logger.Error("Error running REPL", "err", err)
				os.Exit(1)
			}
		}
	}

	root.Cmd.Flags().StringVarP(&patternName, "pattern", "p", "", "Pattern to use")
	root.Cmd.Flags().StringVarP(&modelName, "model", "m", "", "Model to use (overrides default)")
	root.Cmd.Flags().BoolVar(&noStream, "no-stream", false, "Disable streaming (wait for full response)")
	root.Cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print assembled prompt without calling LLM")

	root.Cmd.AddCommand(patternCmd())
	root.Cmd.AddCommand(modelCmd())
	root.Cmd.AddCommand(providerCmd())
	root.Cmd.AddCommand(upgradeCmd())

	return root
}

func patternCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pattern",
		Short: "Manage patterns",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available patterns",
		Run: func(cmd *cobra.Command, args []string) {
			patterns, err := pattern.List(cfg.PatternsPath)
			if err != nil {
				fmt.Println("Error listing patterns:", err)
				return
			}
			fmt.Println("Available patterns:")
			for _, p := range patterns {
				fmt.Printf("- %s\n", p)
			}
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "add <name> [system-prompt]",
		Short: "Add a new pattern",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			sys := ""
			if len(args) > 1 {
				sys = args[1]
			}
			if err := pattern.Create(cfg.PatternsPath, name, sys); err != nil {
				fmt.Println("Error creating pattern:", err)
				return
			}
			fmt.Printf("Pattern %q added to %s\n", name, cfg.PatternsPath)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "import <path> [name]",
		Short: "Import a pattern from a file",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			path := args[0]
			name := ""
			if len(args) > 1 {
				name = args[1]
			}
			if err := pattern.Import(cfg.PatternsPath, path, name); err != nil {
				fmt.Println("Error importing pattern:", err)
				return
			}
			fmt.Println("Pattern imported successfully")
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a pattern",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			if err := pattern.Remove(cfg.PatternsPath, name); err != nil {
				fmt.Println("Error removing pattern:", err)
				return
			}
			fmt.Printf("Pattern %q removed\n", name)
		},
	})

	return cmd
}

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage models",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List common models",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Common models:")
			fmt.Println("- claude-3-5-sonnet-latest")
			fmt.Println("- claude-3-opus-latest")
			fmt.Println("- gpt-4o")
			fmt.Println("- gpt-4o-mini")
			fmt.Println("- o1-preview")
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set-default <model>",
		Short: "Set the default model",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			cfg.Model = args[0]
			if err := cfg.Save(); err != nil {
				fmt.Println("Error saving default model:", err)
				return
			}
			fmt.Printf("Default model set to %q\n", cfg.Model)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Refresh model list (stub)",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Model list refreshed (mock)")
		},
	})

	return cmd
}

func providerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage providers",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List supported providers",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Supported providers (URI schemes):")
			fmt.Println("- anthropic://")
			fmt.Println("- openai://")
			fmt.Println("- openrouter://")
			fmt.Println("- xai://")
			fmt.Println("- groq://")
			fmt.Println("- deepseek://")
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "enable <scheme>",
		Short: "Enable a provider (stub)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Provider %q enabled (mock)\n", args[0])
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "disable <scheme>",
		Short: "Disable a provider (stub)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Provider %q disabled (mock)\n", args[0])
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show <scheme>",
		Short: "Show provider details",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			scheme := args[0]
			envVar := ""
			switch scheme {
			case "anthropic":
				envVar = "ANTHROPIC_API_KEY"
			case "openai":
				envVar = "OPENAI_API_KEY"
			}
			if envVar != "" {
				key := os.Getenv(envVar)
				status := "not set"
				if key != "" {
					status = "configured (masked: " + key[:4] + "...)"
				}
				fmt.Printf("Provider: %s\nAPI Key: %s (%s)\n", scheme, envVar, status)
			} else {
				fmt.Printf("Provider details for %q not implemented yet\n", scheme)
			}
		},
	})

	return cmd
}

func upgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade foo to the latest version",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			stateDir, _ := xdg.StateDir("foo")
			checker := upgrade.New(
				upgrade.WithBinary("foo", "0.1.0"),
				upgrade.WithGitHub("hop-top/foo"),
				upgrade.WithStateDir(stateDir),
			)
			if err := upgrade.RunCLI(ctx, checker, upgrade.CLIOptions{}); err != nil {
				return fmt.Errorf("upgrade: %w", err)
			}
			return nil
		},
	}
}
