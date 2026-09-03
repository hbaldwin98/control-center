package web

import "github.com/hbaldwin98/control-center/host"

// eventSpecView is one declared plugin event, with Type already prefixed so a
// notification rule can use it as a match string.
type eventSpecView struct {
	Type    string           `json:"type"`
	Match   string           `json:"match"`
	Purpose string           `json:"purpose"`
	Fields  []eventFieldView `json:"fields"`
}

type eventFieldView struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
	Path    string `json:"path"`
}

func bindEventSpecs(pluginID string, specs []host.EventSpec) []eventSpecView {
	if len(specs) == 0 {
		return nil
	}
	out := make([]eventSpecView, 0, len(specs))
	for _, s := range specs {
		typ := pluginID + "." + s.Type
		v := eventSpecView{
			Type:    typ,
			Match:   typ,
			Purpose: s.Purpose,
			Fields:  make([]eventFieldView, 0, len(s.Fields)),
		}
		for _, f := range s.Fields {
			v.Fields = append(v.Fields, eventFieldView{
				Name:    f.Name,
				Type:    f.Type,
				Purpose: f.Purpose,
				Path:    "event.payload." + f.Name,
			})
		}
		out = append(out, v)
	}
	return out
}
