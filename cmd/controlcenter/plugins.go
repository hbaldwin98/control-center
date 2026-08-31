package main

import (
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/plugins/hello"
)

// This is the only file in the program that imports plugin packages.
//
// Every plugin is its own Go module, tied in by go.work and depending only on the `host`
// module. CI checks each plugin's full dependency graph and rejects core, application,
// other-plugin, and undeclared project imports, so a boundary violation is a failed
// architectural test rather than a review convention.
func registerPlugins(r *pluginhost.Registry) error {
	return r.RegisterAll(
		hello.New(),
	)
}
