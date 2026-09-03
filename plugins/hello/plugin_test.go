package hello

import "testing"

func TestManifestDeclaresTicked(t *testing.T) {
	t.Parallel()
	m := New().Manifest()
	if len(m.Events) != 1 || m.Events[0].Type != "ticked" {
		t.Fatalf("events = %#v, want ticked", m.Events)
	}
	want := map[string]string{"at": "string", "note": "string", "blobKey": "string", "aiText": "string"}
	if len(m.Events[0].Fields) != len(want) {
		t.Fatalf("fields = %#v", m.Events[0].Fields)
	}
	for _, f := range m.Events[0].Fields {
		if want[f.Name] != f.Type || f.Purpose == "" {
			t.Fatalf("field = %#v", f)
		}
	}
}
