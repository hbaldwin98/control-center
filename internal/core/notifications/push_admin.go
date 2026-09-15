package notifications

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
)

// PushPublicKey returns the VAPID public key exposed by a push-capable channel. The
// key is public; the channel credential never leaves the server.
func (s *Service) PushPublicKey(ctx context.Context, channelID string) (string, error) {
	ch, _, err := s.channelFor(ctx, channelID)
	if err != nil {
		return "", err
	}
	registrar, ok := ch.(PushRegistrar)
	if !ok {
		return "", ErrPushUnsupported
	}
	return registrar.PublicKey(ctx)
}

func (s *Service) RegisterPushSubscription(ctx context.Context, channelID string, sub PushSubscription) error {
	if _, err := actor(ctx); err != nil {
		return err
	}
	if err := validatePushSubscription(sub); err != nil {
		return err
	}
	ch, _, err := s.channelFor(ctx, channelID)
	if err != nil {
		return err
	}
	registrar, ok := ch.(PushRegistrar)
	if !ok {
		return ErrPushUnsupported
	}
	return registrar.RegisterSubscription(ctx, sub)
}

func (s *Service) UnregisterPushSubscription(ctx context.Context, channelID, endpoint string) error {
	if _, err := actor(ctx); err != nil {
		return err
	}
	if err := validatePushEndpoint(endpoint); err != nil {
		return err
	}
	ch, _, err := s.channelFor(ctx, channelID)
	if err != nil {
		return err
	}
	registrar, ok := ch.(PushRegistrar)
	if !ok {
		return ErrPushUnsupported
	}
	return registrar.UnregisterSubscription(ctx, strings.TrimSpace(endpoint))
}

func validatePushSubscription(sub PushSubscription) error {
	if err := validatePushEndpoint(sub.Endpoint); err != nil {
		return err
	}
	if !base64URLValue(sub.Keys.P256DH) || !base64URLValue(sub.Keys.Auth) {
		return ErrInvalidPushSubscription
	}
	return nil
}

func validatePushEndpoint(raw string) error {
	if len(raw) > 4096 {
		return ErrInvalidPushSubscription
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return ErrInvalidPushSubscription
	}
	return nil
}

func base64URLValue(raw string) bool {
	if raw == "" {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(raw)
	return err == nil
}
