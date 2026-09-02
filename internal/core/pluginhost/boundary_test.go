package pluginhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginModulesDependOnlyOnHost walks plugins/ and rejects a plugin whose
// go list -deps includes this project's internal/core, cmd, another plugin, or
// anything other than the host module.
func TestPluginModulesDependOnlyOnHost(t *testing.T) {
	root := findModuleRoot(t)
	dir := filepath.Join(root, "plugins")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		modDir := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(modDir, "go.mod")); err != nil {
			continue
		}
		// -test includes the test binary's own dependencies. A plugin's tests are the
		// easiest place for the boundary to erode -- reaching into core for a fixture
		// is a small, reasonable-looking step -- so they are held to the same rule.
		// The one addition tests may use is host/hosttest.
		cmd := exec.Command("go", "list", "-deps", "-test")
		cmd.Dir = modDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: go list -deps -test: %v\n%s", e.Name(), err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.Contains(line, "github.com/hbaldwin98/control-center/internal/") ||
				strings.Contains(line, "github.com/hbaldwin98/control-center/cmd/") {
				t.Errorf("%s imports core or cmd: %s", e.Name(), line)
			}
			if strings.HasPrefix(line, "github.com/hbaldwin98/control-center/plugins/") &&
				!strings.HasPrefix(line, "github.com/hbaldwin98/control-center/plugins/"+e.Name()) {
				t.Errorf("%s imports another plugin: %s", e.Name(), line)
			}
			if line == "github.com/hbaldwin98/control-center" {
				t.Errorf("%s imports the application module: %s", e.Name(), line)
			}
			low := strings.ToLower(line)
			if strings.Contains(low, "playwright") || strings.Contains(low, "chromedp") ||
				strings.Contains(low, "go-rod") || strings.HasSuffix(low, "/rod") {
				t.Errorf("%s imports a browser engine; use host/browser: %s", e.Name(), line)
			}
		}
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "DESIGN.md")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found")
		}
		dir = parent
	}
}
