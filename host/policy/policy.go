// Package policy is the plugin-facing surface of enabled-state and budget decisions.
package policy

import "errors"

// MicroUSD is integer micro-USD. All money is integer; negative values are invalid.
type MicroUSD int64

// Errors a plugin must handle. Both mean stop cleanly, not retry.
var (
	ErrPluginDisabled = errors.New("plugin disabled")
	ErrBudgetExceeded = errors.New("budget exceeded")
)
