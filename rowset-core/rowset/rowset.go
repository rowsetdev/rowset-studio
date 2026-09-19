// Package rowset builds a Rowset executable. It is the package other Go
// modules import to run Rowset with their own plugins: a plugin adds commands,
// HTTP routes, statement and result hooks, an escalation workflow and
// connection fields through Setup. Everything else in this module is internal.
package rowset

import (
	"log/slog"
	"os"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/api"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/app"
)

// Plugin extends a Rowset executable.
type Plugin interface {
	Setup(*Setup) error
}

// PluginFunc adapts a function to Plugin.
type PluginFunc func(*Setup) error

func (f PluginFunc) Setup(setup *Setup) error { return f(setup) }

// Options describes a Rowset executable.
type Options struct {
	// Version is printed by --version and shown in Studio.
	Version string
	Plugins []Plugin
}

// Main runs the command named on the command line and exits.
func Main(options Options) {
	if err := Run(os.Args[1:], options); err != nil {
		slog.Error("rowset stopped", "error", err)
		os.Exit(1)
	}
}

// Run runs the command named in args with the given plugins.
func Run(args []string, options Options) error {
	setup := &Setup{options: app.Options{Version: options.Version, Commands: map[string]app.Command{}}}
	for _, plugin := range options.Plugins {
		if err := plugin.Setup(setup); err != nil {
			return err
		}
	}
	return app.Run(args, setup.options)
}

// Setup collects what plugins add to the executable.
type Setup struct {
	options app.Options
}

// Command is a subcommand a plugin adds.
type Command = app.Command

// Context is what a command receives.
type Context = app.Context

// AddCommand adds a subcommand; a later plugin replaces an earlier command
// of the same name.
func (s *Setup) AddCommand(name string, command Command) {
	s.options.Commands[name] = command
}

// SetDefaultCommand names the command that runs when none is given.
func (s *Setup) SetDefaultCommand(name string) { s.options.DefaultCommand = name }

// ConfigureServer runs for every server the executable creates, before it
// serves requests.
func (s *Setup) ConfigureServer(configure func(*Server) error) {
	s.options.ConfigureServer = append(s.options.ConfigureServer, configure)
}

// Server is a running Rowset API server.
type Server = api.Server

// ServerOptions describe a server a command creates with Context.NewServer.
type ServerOptions = app.ServerOptions
