package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// GetConfig returns the persisted document, or the manifest defaults if none exists.
func (r *Registry) GetConfig(ctx context.Context, pluginID string) (json.RawMessage, error) {
	s, ok := r.lookup(pluginID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPlugin, pluginID)
	}
	raw, _, err := r.loadConfig(ctx, s)
	return raw, err
}

// UpdateConfig validates, persists, and notifies only the active enabled generation.
func (r *Registry) UpdateConfig(ctx context.Context, pluginID string, value json.RawMessage, actor string) error {
	s, ok := r.lookup(pluginID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPlugin, pluginID)
	}
	if actor == "" {
		actor = "admin"
	}
	if err := validateAgainstSchema(s.decl.manifest.Config, value); err != nil {
		return err
	}
	now := rfc3339(r.now())
	err := r.opts.DB.Tx(ctx, func(tx storage.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO core_plugin_config(plugin_id, value_json, version, updated_at, updated_by)
			 VALUES (?, ?, 1, ?, ?)
			 ON CONFLICT(plugin_id) DO UPDATE SET
			     value_json = excluded.value_json,
			     version    = core_plugin_config.version + 1,
			     updated_at = excluded.updated_at,
			     updated_by = excluded.updated_by`,
			pluginID, string(value), now, actor)
		return err
	})
	if err != nil {
		return err
	}
	s.notifyConfig(value)
	return nil
}

func validateAgainstSchema(spec host.ConfigSpec, value json.RawMessage) error {
	if len(spec.Schema) == 0 || string(spec.Schema) == "null" {
		if len(value) == 0 || string(value) == "null" || string(value) == "{}" {
			return nil
		}
		return fmt.Errorf("%w: this plugin has no config schema", ErrInvalidConfig)
	}
	var schema any
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	var v any
	if err := json.Unmarshal(value, &v); err != nil {
		return fmt.Errorf("%w: value is not JSON: %v", ErrInvalidConfig, err)
	}
	return validateAgainst(schema, v, "")
}

func (r *Registry) loadConfig(ctx context.Context, s *slot) (json.RawMessage, int64, error) {
	var raw string
	var version int64
	err := r.opts.DB.QueryRow(ctx,
		`SELECT value_json, version FROM core_plugin_config WHERE plugin_id = ?`,
		s.decl.manifest.ID).Scan(&raw, &version)
	if storage.IsNoRows(err) {
		def := s.decl.manifest.Config.Defaults
		if len(def) == 0 || string(def) == "null" {
			def = json.RawMessage(`{}`)
		}
		return def, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return json.RawMessage(raw), version, nil
}

func (r *Registry) ensureConfigRow(ctx context.Context, s *slot) error {
	id := s.decl.manifest.ID
	def := s.decl.manifest.Config.Defaults
	if len(def) == 0 || string(def) == "null" {
		def = json.RawMessage(`{}`)
	}
	_, err := r.opts.DB.Exec(ctx,
		`INSERT INTO core_plugin_config(plugin_id, value_json, version, updated_at, updated_by)
		 VALUES (?, ?, 1, ?, 'system')
		 ON CONFLICT(plugin_id) DO NOTHING`,
		id, string(def), rfc3339(r.now()))
	return err
}

func (r *Registry) validatePersistedConfig(ctx context.Context, s *slot) error {
	raw, _, err := r.loadConfig(ctx, s)
	if err != nil {
		return err
	}
	if len(s.decl.manifest.Config.Schema) == 0 || string(s.decl.manifest.Config.Schema) == "null" {
		return nil
	}
	return validateAgainstSchema(s.decl.manifest.Config, raw)
}

type pluginConfig struct {
	mu        sync.Mutex
	slot      *slot
	value     json.RawMessage
	listeners []func(context.Context, json.RawMessage)
}

func (c *pluginConfig) Decode(dst any) error {
	c.mu.Lock()
	raw := append(json.RawMessage(nil), c.value...)
	c.mu.Unlock()
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return json.Unmarshal(raw, dst)
}

func (c *pluginConfig) Watch(fn func(context.Context, json.RawMessage)) func() {
	if fn == nil {
		return func() {}
	}
	c.mu.Lock()
	idx := len(c.listeners)
	c.listeners = append(c.listeners, fn)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if idx < len(c.listeners) {
			c.listeners[idx] = nil
		}
		c.mu.Unlock()
	}
}

func (s *slot) notifyConfig(value json.RawMessage) {
	s.mu.Lock()
	h, _ := s.host.(*scopedHost)
	gen := s.gen
	initialized := s.initialized
	s.mu.Unlock()
	if !initialized || h == nil || gen == nil {
		return
	}
	h.config.mu.Lock()
	h.config.value = append(json.RawMessage(nil), value...)
	fns := append([]func(context.Context, json.RawMessage){}, h.config.listeners...)
	h.config.mu.Unlock()
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		go func(fn func(context.Context, json.RawMessage)) {
			defer func() { _ = recover() }()
			fn(gen, value)
		}(fn)
	}
}
