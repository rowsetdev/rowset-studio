// Package app is the Rowset executable: its commands, the desktop workspace
// and the server set-up shared by every command.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/activity"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/api"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// Command runs one subcommand.
type Command struct {
	// Usage is a one-line description for --help.
	Usage string
	Run   func(Context) error
}

// Context is what a command receives.
type Context struct {
	// ConfigPath is the --config argument, if one was given.
	ConfigPath string
	Version    string
	options    *Options
}

// ConfigureServer applies the registered server set-up to a server the
// command created; call it before the server's Handler.
func (c Context) ConfigureServer(server *api.Server) error {
	for _, configure := range c.options.ConfigureServer {
		if err := configure(server); err != nil {
			return err
		}
	}
	return nil
}

// ServerOptions describe a server a command creates.
type ServerOptions struct {
	Config config.Config
	Store  *store.Store
	// Activity stores audit entries and query history; nil keeps them in
	// the control database.
	Activity activity.Store
	Logger   *slog.Logger
}

// NewServer creates a server with the registered server set-up applied.
func (c Context) NewServer(options ServerOptions) (*api.Server, error) {
	if options.Activity == nil {
		// Statements are recorded by one background writer in batches, so an
		// audit write never holds up the statement it describes.
		options.Activity = activity.NewBuffered(activity.SQLite{Data: options.Store}, 4096)
	}
	server := api.NewWithActivity(options.Config, options.Store, options.Activity, auth.NewIssuer(options.Config.JWTSecret, options.Config.JWTSecretPrevious), options.Logger)
	if err := c.ConfigureServer(server); err != nil {
		server.Close()
		return nil, err
	}
	return server, nil
}

// Options describes one Rowset executable.
type Options struct {
	Version string
	// DefaultCommand runs when no command is given; empty means desktop.
	DefaultCommand string
	Commands       map[string]Command
	// ConfigureServer runs, in order, for every server the executable creates.
	ConfigureServer []func(*api.Server) error
}

// Run runs the command named in args.
func Run(args []string, options Options) error {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.DefaultCommand == "" {
		options.DefaultCommand = "desktop"
	}
	command, configPath, err := parseArgs(args, options.DefaultCommand)
	if err != nil {
		return err
	}
	if configPath != "" {
		if err := os.Setenv("ROWSET_CONFIG", configPath); err != nil {
			return err
		}
	}
	ctx := Context{ConfigPath: configPath, Version: options.Version, options: &options}
	switch command {
	case "--version", "-V", "version":
		fmt.Printf("rowset %s\n", options.Version)
		return nil
	case "--help", "-h", "help":
		printHelp(options)
		return nil
	case "desktop":
		return desktop(ctx)
	case "desktop-stop":
		return stopDesktop()
	}
	if handler, ok := options.Commands[command]; ok {
		return handler.Run(ctx)
	}
	return fmt.Errorf("unknown command %q; run rowset --help", command)
}

func parseArgs(args []string, defaultCommand string) (string, string, error) {
	command, configPath := defaultCommand, ""
	commandSet := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--config":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return "", "", errors.New("--config requires a path")
			}
			configPath = args[index]
		case strings.HasPrefix(argument, "--config="):
			configPath = strings.TrimPrefix(argument, "--config=")
			if strings.TrimSpace(configPath) == "" {
				return "", "", errors.New("--config requires a path")
			}
		case !commandSet:
			command, commandSet = argument, true
		default:
			return "", "", fmt.Errorf("unexpected argument %q", argument)
		}
	}
	return command, configPath, nil
}

func printHelp(options Options) {
	fmt.Printf("rowset %s\n\nUSAGE:\n  rowset desktop        start and open Rowset Studio\n  rowset desktop-stop   stop the running Rowset Studio\n  rowset --version\n", options.Version)
	if len(options.Commands) == 0 {
		return
	}
	names := make([]string, 0, len(options.Commands))
	for name := range options.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("  rowset %-14s %s\n", name, options.Commands[name].Usage)
	}
	fmt.Printf("\nDefault command: %s\n", options.DefaultCommand)
}
