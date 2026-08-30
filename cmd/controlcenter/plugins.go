package main

import "context"

// This is the only file in the program that imports plugin packages.
//
// Every plugin is its own Go module, tied in by go.work and depending only on the `host`
// module. CI checks each plugin's full dependency graph and rejects core, application,
// other-plugin, and undeclared project imports, so a boundary violation is a failed
// architectural test rather than a review convention.
//
// Plugins arrive at milestone 6; until then this registers nothing.
func registerPlugins(_ context.Context) error {
	return nil
}
