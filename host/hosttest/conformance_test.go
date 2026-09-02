package hosttest_test

import (
	"testing"

	"github.com/hbaldwin98/control-center/host/hosttest"
)

// The double must pass the same suite the real host does. This is the cheap half of
// that guarantee; the expensive half runs in internal/core/pluginhost, against the
// real facade.
func TestConformance(t *testing.T) {
	hosttest.Conformance(t, func(t *testing.T, probe *hosttest.Probe) hosttest.Driver {
		return hosttest.NewDriver(hosttest.New(t, probe))
	})
}
