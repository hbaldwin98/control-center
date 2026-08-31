package ai

import (
	"context"
	"fmt"
)

// Models returns what a provider offers.
//
// The catalog is discovered from the provider, not declared here. A cached answer is
// served unless refresh is asked for or nothing has been fetched yet, so opening the
// Settings screen does not call out to every configured provider.
//
// Discovery is advisory. Routes name their models explicitly, so a provider that is
// unreachable, or that stops listing a model, breaks the picker rather than a running
// route.
func (s *Service) Models(ctx context.Context, providerID string, refresh bool) ([]Model, error) {
	providers, err := s.loadProviders(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := providers[providerID]
	if !ok {
		return nil, ErrUnknownProvider
	}
	if !refresh {
		cached, err := s.readCatalog(ctx, providerID)
		if err != nil {
			return nil, err
		}
		if len(cached) > 0 {
			return cached, nil
		}
	}
	adapter, ok := s.adapters[p.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: no adapter for kind %q", ErrUnknownProvider, p.Kind)
	}
	d, err := s.dispatchFor(ctx, attempt{
		providerID: p.ID, kind: p.Kind, baseURL: p.BaseURL,
		credential: p.CredentialID, billing: p.Billing,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDiscovery, providerID, err)
	}
	models, err := adapter.Models(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDiscovery, providerID, err)
	}
	for i := range models {
		models[i].Provider = providerID
	}
	if err := s.saveCatalog(ctx, providerID, models); err != nil {
		return nil, err
	}
	// Read back so callers see the stored fetch time rather than a zero one.
	return s.readCatalog(ctx, providerID)
}
