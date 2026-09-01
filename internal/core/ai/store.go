package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// credentialRefOwner is the reference name credentials records against every credential
// a configured provider uses. It blocks deleting a credential a provider still needs.
const credentialRefOwner = "ai.providers"

func (s *Service) loadProviders(ctx context.Context) (map[string]ProviderConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, kind, base_url, credential_id, billing FROM core_ai_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProviderConfig{}
	for rows.Next() {
		var p ProviderConfig
		var kind, billing string
		if err := rows.Scan(&p.ID, &kind, &p.BaseURL, &p.CredentialID, &billing); err != nil {
			return nil, err
		}
		p.Kind, p.Billing = ProviderKind(kind), Billing(billing)
		out[p.ID] = p
	}
	return out, rows.Err()
}

func (s *Service) loadRouteInputs(ctx context.Context) ([]RouteInput, error) {
	rows, err := s.db.Query(ctx,
		`SELECT name, capabilities, max_input_tokens, max_output_tokens FROM core_ai_routes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteInput
	for rows.Next() {
		var in RouteInput
		var caps string
		if err := rows.Scan(&in.Name, &caps, &in.MaxInputTokens, &in.MaxOutputTokens); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(caps), &in.Capabilities); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		att, err := s.loadRouteAttempts(ctx, out[i].Name)
		if err != nil {
			return nil, err
		}
		out[i].Attempts = att
	}
	return out, nil
}

func (s *Service) loadRouteAttempts(ctx context.Context, name string) ([]RouteAttemptInput, error) {
	rows, err := s.db.Query(ctx,
		`SELECT provider_id, model, input_micro_usd_per_million, output_micro_usd_per_million
		   FROM core_ai_route_attempts WHERE route_name = ? ORDER BY ordinal`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteAttemptInput
	for rows.Next() {
		var a RouteAttemptInput
		if err := rows.Scan(&a.Provider, &a.Model, &a.InputMicroUSDPerMillion, &a.OutputMicroUSDPerMillion); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// reload rebuilds the compiled route table from the database and republishes the set of
// credentials providers depend on.
//
// A route that no longer compiles — its provider was deleted, its pricing was cleared —
// is kept and marked, not dropped. Dropping it would make a plugin's model name resolve
// to "unknown route", which reads as a plugin bug; keeping it says which route is
// broken and why.
func (s *Service) reload(ctx context.Context) error {
	providers, err := s.loadProviders(ctx)
	if err != nil {
		return err
	}
	inputs, err := s.loadRouteInputs(ctx)
	if err != nil {
		return err
	}
	compiled := make([]route, 0, len(inputs))
	for _, in := range inputs {
		r, err := compileRoute(in, providers)
		if err != nil {
			r.lastError = err.Error()
		}
		compiled = append(compiled, r)
	}

	s.mu.Lock()
	s.providerCfg = providers
	s.routes = compiled
	s.mu.Unlock()

	if s.refs == nil {
		return nil
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		if _, ok := seen[p.CredentialID]; ok {
			continue
		}
		seen[p.CredentialID] = struct{}{}
		ids = append(ids, p.CredentialID)
	}
	sort.Strings(ids)
	return s.refs.Replace(ctx, credentialRefOwner, ids)
}

// Providers lists the configured upstreams.
func (s *Service) Providers(ctx context.Context) ([]ProviderConfig, error) {
	m, err := s.loadProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderConfig, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// PutProvider creates or replaces a provider. The credential must already exist and,
// for a subscription provider, must be the OAuth credential that carries an account id;
// an API key cannot authorize a subscription backend and would fail on every call.
func (s *Service) PutProvider(ctx context.Context, p ProviderConfig) error {
	p = p.normalize()
	if err := p.validate(); err != nil {
		return err
	}
	attrs, err := s.creds.Attributes(ctx, p.CredentialID)
	if err != nil {
		return fmt.Errorf("%w: credential %q: %v", ErrMissingCredential, p.CredentialID, err)
	}
	if p.Kind == KindCodex && attrs.Kind != "oauth" {
		return fmt.Errorf("%w: credential %q is an api key", ErrSubscriptionOnly, p.CredentialID)
	}
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		now := rfc(s.now())
		_, err := tx.Exec(ctx,
			`INSERT INTO core_ai_providers(id, kind, base_url, credential_id, billing, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			     kind = excluded.kind, base_url = excluded.base_url,
			     credential_id = excluded.credential_id, billing = excluded.billing,
			     updated_at = excluded.updated_at`,
			p.ID, string(p.Kind), p.BaseURL, p.CredentialID, string(p.Billing), now, now)
		return err
	})
	if err != nil {
		return err
	}
	return s.reload(ctx)
}

// DeleteProvider removes a provider no route depends on. Removing one a route still
// names would turn that route into a compile failure the administrator did not ask for.
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	err := s.db.Tx(ctx, func(tx storage.Tx) error {
		var exists string
		if err := tx.QueryRow(ctx, `SELECT id FROM core_ai_providers WHERE id = ?`, id).Scan(&exists); err != nil {
			if storage.IsNoRows(err) {
				return ErrUnknownProvider
			}
			return err
		}
		var users []string
		rows, err := tx.Query(ctx,
			`SELECT DISTINCT route_name FROM core_ai_route_attempts WHERE provider_id = ? ORDER BY route_name`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			users = append(users, name)
		}
		rows.Close()
		if len(users) > 0 {
			return fmt.Errorf("%w: %s", ErrProviderInUse, strings.Join(users, ", "))
		}
		if _, err := tx.Exec(ctx, `DELETE FROM core_ai_catalog WHERE provider_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM core_ai_providers WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	return s.reload(ctx)
}

// PutRoute creates or replaces a logical route. It must compile against the current
// providers before it is stored, so an unusable route can only come from a later change
// to something it depends on, never from this call.
func (s *Service) PutRoute(ctx context.Context, in RouteInput) error {
	in.Name = strings.TrimSpace(in.Name)
	providers, err := s.loadProviders(ctx)
	if err != nil {
		return err
	}
	if _, err := compileRoute(in, providers); err != nil {
		return err
	}
	caps, err := json.Marshal(in.Capabilities)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		now := rfc(s.now())
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_ai_routes(name, capabilities, max_input_tokens, max_output_tokens, updated_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET
			     capabilities = excluded.capabilities,
			     max_input_tokens = excluded.max_input_tokens,
			     max_output_tokens = excluded.max_output_tokens,
			     updated_at = excluded.updated_at`,
			in.Name, string(caps), in.MaxInputTokens, in.MaxOutputTokens, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM core_ai_route_attempts WHERE route_name = ?`, in.Name); err != nil {
			return err
		}
		for i, a := range in.Attempts {
			if _, err := tx.Exec(ctx,
				`INSERT INTO core_ai_route_attempts(route_name, ordinal, provider_id, model,
				     input_micro_usd_per_million, output_micro_usd_per_million)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				in.Name, i+1, a.Provider, a.Model,
				a.InputMicroUSDPerMillion, a.OutputMicroUSDPerMillion); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *Service) DeleteRoute(ctx context.Context, name string) error {
	err := s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM core_ai_route_attempts WHERE route_name = ?`, name); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM core_ai_routes WHERE name = ?`, name)
		return err
	})
	if err != nil {
		return err
	}
	return s.reload(ctx)
}

// seed writes a starting configuration into empty tables. It is a no-op the moment the
// administrator has configured anything, so a file left on disk cannot resurrect a
// provider or route that was deleted from the UI.
func (s *Service) seed(ctx context.Context, in Seed) error {
	if len(in.Providers) == 0 && len(in.Routes) == 0 {
		return nil
	}
	var providers, routes int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM core_ai_providers`).Scan(&providers); err != nil {
		return err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM core_ai_routes`).Scan(&routes); err != nil {
		return err
	}
	if providers > 0 || routes > 0 {
		return nil
	}
	for _, p := range in.Providers {
		if err := s.PutProvider(ctx, p); err != nil {
			return err
		}
	}
	for _, r := range in.Routes {
		if err := s.PutRoute(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) saveCatalog(ctx context.Context, providerID string, models []Model) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM core_ai_catalog WHERE provider_id = ?`, providerID); err != nil {
			return err
		}
		now := rfc(s.now())
		for _, m := range models {
			priced := 0
			if m.Priced {
				priced = 1
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO core_ai_catalog(provider_id, model, display_name, context_window, max_output_tokens,
				     input_micro_usd_per_million, output_micro_usd_per_million, priced, fetched_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				providerID, m.ID, m.DisplayName, m.ContextWindow, m.MaxOutputTokens,
				int64(m.InputMicroUSDPerMillion), int64(m.OutputMicroUSDPerMillion), priced, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) readCatalog(ctx context.Context, providerID string) ([]Model, error) {
	rows, err := s.db.Query(ctx,
		`SELECT model, display_name, context_window, max_output_tokens,
		        input_micro_usd_per_million, output_micro_usd_per_million, priced, fetched_at
		   FROM core_ai_catalog WHERE provider_id = ? ORDER BY model`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		m := Model{Provider: providerID}
		var in, outPrice int64
		var priced int
		var fetched string
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.ContextWindow, &m.MaxOutputTokens,
			&in, &outPrice, &priced, &fetched); err != nil {
			return nil, err
		}
		m.InputMicroUSDPerMillion = policy.MicroUSD(in)
		m.OutputMicroUSDPerMillion = policy.MicroUSD(outPrice)
		m.Priced = priced == 1
		if t, err := parseTime(fetched); err == nil {
			m.FetchedAt = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
