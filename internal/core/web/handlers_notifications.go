package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/notifications"
)

func (s *Server) handleInboxList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	q := notifications.InboxQuery{UnreadOnly: r.URL.Query().Get("unread") == "1", AfterID: r.URL.Query().Get("after")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit must be 1..200")
			return
		}
		q.Limit = n
	}
	page, err := s.deps.Notifications.List(r.Context(), q)
	if err != nil {
		s.fail(w, "list inbox", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, page, formatID(tail))
}

func (s *Server) handleInboxGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	n, err := s.deps.Notifications.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, notifications.ErrUnknownNotif) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such notification")
		return
	}
	if err != nil {
		s.fail(w, "get notification", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, n, formatID(tail))
}

func (s *Server) handleInboxRead(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	var req struct {
		Read bool `json:"read"`
	}
	req.Read = true
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, 1<<10, &req) {
			return
		}
	}
	err := s.deps.Notifications.MarkRead(r.Context(), r.PathValue("id"), req.Read)
	if errors.Is(err, notifications.ErrUnknownNotif) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such notification")
		return
	}
	if err != nil {
		s.fail(w, "mark read", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNotifRules(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	rules, err := s.deps.Notifications.ListRules(r.Context())
	if err != nil {
		s.fail(w, "list rules", err)
		return
	}
	out := make([]ruleWire, 0, len(rules))
	for _, rule := range rules {
		out = append(out, toRuleWire(rule))
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, out, formatID(tail))
}

func (s *Server) handleNotifRulePut(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	var wire ruleWire
	if !decodeJSON(w, r, 32<<10, &wire) {
		return
	}
	wire.ID = r.PathValue("id")
	err := s.deps.Notifications.PutRule(notifications.WithActor(r.Context(), actorAdmin), fromRuleWire(wire))
	s.writeNotifAdminErr(w, "put rule", err)
}

func (s *Server) handleNotifRuleDelete(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	err := s.deps.Notifications.DeleteRule(notifications.WithActor(r.Context(), actorAdmin), r.PathValue("id"))
	if errors.Is(err, notifications.ErrUnknownRule) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such rule")
		return
	}
	s.writeNotifAdminErr(w, "delete rule", err)
}

func (s *Server) handleNotifChannels(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	ch, err := s.deps.Notifications.ListChannels(r.Context())
	if err != nil {
		s.fail(w, "list channels", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, ch, formatID(tail))
}

func (s *Server) handleNotifChannelPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	var c notifications.ChannelConfig
	if !decodeJSON(w, r, 32<<10, &c) {
		return
	}
	c.ID = r.PathValue("id")
	err := s.deps.Notifications.PutChannel(notifications.WithActor(r.Context(), actorAdmin), c)
	s.writeNotifAdminErr(w, "put channel", err)
}

func (s *Server) handleNotifChannelDelete(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	err := s.deps.Notifications.DeleteChannel(notifications.WithActor(r.Context(), actorAdmin), r.PathValue("id"))
	if errors.Is(err, notifications.ErrUnknownChannel) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such channel")
		return
	}
	s.writeNotifAdminErr(w, "delete channel", err)
}

func (s *Server) handleNotifPushKey(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	key, err := s.deps.Notifications.PushPublicKey(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeNotifPushErr(w, "get push key", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": key})
}

func (s *Server) handleNotifPushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	var sub notifications.PushSubscription
	if !decodeJSON(w, r, 16<<10, &sub) {
		return
	}
	err := s.deps.Notifications.RegisterPushSubscription(
		notifications.WithActor(r.Context(), actorAdmin), r.PathValue("id"), sub,
	)
	if err != nil {
		s.writeNotifPushErr(w, "register push subscription", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNotifPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSON(w, r, 8<<10, &req) {
		return
	}
	err := s.deps.Notifications.UnregisterPushSubscription(
		notifications.WithActor(r.Context(), actorAdmin), r.PathValue("id"), req.Endpoint,
	)
	if err != nil {
		s.writeNotifPushErr(w, "remove push subscription", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeNotifPushErr(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, notifications.ErrUnknownChannel):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such channel")
	case errors.Is(err, notifications.ErrPushUnsupported), errors.Is(err, notifications.ErrInvalidPushSubscription):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	default:
		s.fail(w, op, err)
	}
}

func (s *Server) handleNotifHealth(w http.ResponseWriter, r *http.Request) {
	if s.deps.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "notifications unavailable")
		return
	}
	h, err := s.deps.Notifications.DeliveryHealth(r.Context())
	if err != nil {
		s.fail(w, "delivery health", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, h, formatID(tail))
}

func (s *Server) writeNotifAdminErr(w http.ResponseWriter, op string, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, notifications.ErrNoActor):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
	case errors.Is(err, notifications.ErrInvalidRule), errors.Is(err, notifications.ErrInvalidChannel),
		errors.Is(err, notifications.ErrInvalidURL), errors.Is(err, notifications.ErrInvalidTemplate),
		errors.Is(err, notifications.ErrInvalidWhere):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, notifications.ErrChannelInUse):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, notifications.ErrDuplicateID):
		writeError(w, http.StatusConflict, CodeConflict, "id already exists")
	default:
		s.fail(w, op, err)
	}
}

type ruleWire struct {
	ID              string   `json:"id"`
	Enabled         bool     `json:"enabled"`
	Match           string   `json:"match"`
	Where           string   `json:"where"`
	Channels        []string `json:"channels"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	URL             string   `json:"url"`
	ThrottleSeconds int64    `json:"throttleSeconds"`
}

func toRuleWire(r notifications.Rule) ruleWire {
	return ruleWire{
		ID: r.ID, Enabled: r.Enabled, Match: r.Match, Where: r.Where,
		Channels: r.Channels, Title: r.Title, Body: r.Body, URL: r.URL,
		ThrottleSeconds: int64(r.Throttle / time.Second),
	}
}

func fromRuleWire(w ruleWire) notifications.Rule {
	return notifications.Rule{
		ID: w.ID, Enabled: w.Enabled, Match: w.Match, Where: w.Where,
		Channels: w.Channels, Title: w.Title, Body: w.Body, URL: w.URL,
		Throttle: time.Duration(w.ThrottleSeconds) * time.Second,
	}
}
