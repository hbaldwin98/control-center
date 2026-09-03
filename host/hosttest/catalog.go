package hosttest

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/host"
)

// CheckEventCatalog asserts that a plugin's declared events and its published
// payloads describe the same thing, in both directions: every declared path
// resolves against a real payload, and every key a payload carries is declared.
// It is the guard that keeps a hand-maintained catalog from drifting away from
// the payloads once host.Fields has derived it.
//
// samples maps each declared event type to a fully populated payload — every
// optional field set, because the catalog promises those paths exist. An event
// with no sample fails: a payload nobody exercises is exactly where drift hides.
func CheckEventCatalog(t *testing.T, m host.Manifest, samples map[string]any) {
	t.Helper()
	for _, spec := range m.Events {
		sample, ok := samples[spec.Type]
		if !ok {
			t.Errorf("%s event %q has no sample payload; add one so its fields stay checked", m.ID, spec.Type)
			continue
		}
		payload, ok := asMap(t, m.ID, spec.Type, sample)
		if !ok {
			continue
		}
		declared := map[string]bool{}
		for _, f := range spec.Fields {
			declared[f.Name] = true
			if strings.TrimSpace(f.Purpose) == "" {
				t.Errorf("%s %s field %q has no purpose, so the plugin will not load", m.ID, spec.Type, f.Name)
			}
			if resolvePath(payload, f.Name) == nil {
				t.Errorf("%s %s declares %q but the sample payload has no such path", m.ID, spec.Type, f.Name)
			}
		}
		for _, key := range sortedKeys(payload) {
			if !declared[key] {
				t.Errorf("%s %s publishes %q but the catalog does not declare it", m.ID, spec.Type, key)
			}
		}
	}
	for typ := range samples {
		if !declares(m.Events, typ) {
			t.Errorf("%s has a sample payload for %q but declares no such event", m.ID, typ)
		}
	}
}

func declares(specs []host.EventSpec, typ string) bool {
	for _, s := range specs {
		if s.Type == typ {
			return true
		}
	}
	return false
}

func asMap(t *testing.T, pluginID, typ string, sample any) (map[string]any, bool) {
	t.Helper()
	raw, err := json.Marshal(sample)
	if err != nil {
		t.Errorf("%s %s payload does not marshal: %v", pluginID, typ, err)
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Errorf("%s %s payload is not a JSON object: %v", pluginID, typ, err)
		return nil, false
	}
	return out, true
}

// resolvePath walks a dotted payload path the way a notification template does.
func resolvePath(payload map[string]any, path string) any {
	var cur any = payload
	for part := range strings.SplitSeq(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func sortedKeys(payload map[string]any) []string {
	out := make([]string, 0, len(payload))
	for key := range payload {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
