package web

import (
	"net/http"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

type eventCatalog struct {
	Envelope []envelopeField `json:"envelope"`
	Events   []catalogEvent  `json:"events"`
}

type envelopeField struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

type catalogEvent struct {
	Source  string           `json:"source"`
	Name    string           `json:"name"`
	Type    string           `json:"type"`
	Match   string           `json:"match"`
	Purpose string           `json:"purpose"`
	Fields  []eventFieldView `json:"fields"`
}

func catalogEnvelope() []envelopeField {
	return []envelopeField{
		{Path: "event.type", Type: "string", Purpose: "Fully qualified type, such as tid.synced."},
		{Path: "event.subject", Type: "string", Purpose: "Stable entity id, or a short headline."},
		{Path: "event.source", Type: "string", Purpose: "Plugin id, or core.<module>."},
		{Path: "event.id", Type: "number", Purpose: "Monotonic id in the event log."},
		{Path: "collapsed", Type: "number", Purpose: "How many events a throttle window folded together."},
	}
}

func coreCatalogEvents() []catalogEvent {
	body := eventFieldView{
		Name: "body", Type: "string", Purpose: "Plain-text details for the inbox and ntfy.",
		Path: "event.payload.body",
	}
	return []catalogEvent{
		{
			Source: "*", Name: "Plugins", Type: "*.alert", Match: "*.alert",
			Purpose: "Any plugin alert. The default plugin-alert rule uses this pattern.",
			Fields:  []eventFieldView{body},
		},
		{
			Source: "core", Name: "Jobs", Type: events.TypeJobDead, Match: events.TypeJobDead,
			Purpose: "A job exhausted its retries.",
		},
		{
			Source: "core", Name: "Policy", Type: events.TypePluginBudgetExceeded, Match: events.TypePluginBudgetExceeded,
			Purpose: "A budget admission was rejected.",
		},
		{
			Source: "core", Name: "Policy", Type: events.TypePluginAccountingInvariantFailed, Match: events.TypePluginAccountingInvariantFailed,
			Purpose: "Provider cost exceeded its conservative reservation.",
		},
		{
			Source: "core", Name: "Credentials", Type: events.TypeCredentialNeedsReauth, Match: events.TypeCredentialNeedsReauth,
			Purpose: "A credential needs a human.",
		},
		{
			Source: "core", Name: "Events", Type: events.TypeSubscriptionPaused, Match: events.TypeSubscriptionPaused,
			Purpose: "A durable subscriber stopped on a poison event.",
		},
	}
}

func (s *Server) handleNotifCatalog(w http.ResponseWriter, r *http.Request) {
	cat := eventCatalog{Envelope: catalogEnvelope(), Events: coreCatalogEvents()}
	if s.deps.PluginHost != nil {
		for _, d := range s.deps.PluginHost.List() {
			for _, ev := range bindEventSpecs(d.Manifest.ID, d.Manifest.Events) {
				cat.Events = append(cat.Events, catalogEvent{
					Source:  d.Manifest.ID,
					Name:    d.Manifest.Name,
					Type:    ev.Type,
					Match:   ev.Match,
					Purpose: ev.Purpose,
					Fields:  ev.Fields,
				})
			}
		}
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	for i := range cat.Events {
		if cat.Events[i].Fields == nil {
			cat.Events[i].Fields = []eventFieldView{}
		}
	}
	writeSnapshot(w, cat, formatID(tail))
}
