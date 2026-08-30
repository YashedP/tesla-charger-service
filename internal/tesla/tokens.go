package tesla

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"tesla-charger-service/internal/store"
)

// Tokens serializes read-refresh-save with OAuth replacement. Clients receive
// static token sources so an implicit HTTP refresh cannot lose rotated tokens.
type Tokens struct {
	store store.TokenStore
	oauth *oauth2.Config
	gate  chan struct{}
}

func NewTokens(tokens store.TokenStore, cfg *oauth2.Config) *Tokens {
	return &Tokens{store: tokens, oauth: cfg, gate: make(chan struct{}, 1)}
}

func (t *Tokens) lock(ctx context.Context) error {
	select {
	case t.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Tokens) Fresh(ctx context.Context) (*oauth2.Token, error) {
	if err := t.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-t.gate }()
	old, err := t.store.LoadToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}
	fresh, err := t.oauth.TokenSource(ctx, old).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = old.RefreshToken
	}
	if old.AccessToken != fresh.AccessToken || old.RefreshToken != fresh.RefreshToken || old.TokenType != fresh.TokenType || !old.Expiry.Equal(fresh.Expiry) {
		if err := t.store.SaveToken(ctx, fresh); err != nil {
			return nil, fmt.Errorf("persist refreshed token: %w", err)
		}
	}
	return fresh, nil
}

func (t *Tokens) Exchange(ctx context.Context, code, audience string) error {
	if err := t.lock(ctx); err != nil {
		return err
	}
	defer func() { <-t.gate }()
	tok, err := t.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("audience", audience))
	if err != nil {
		return fmt.Errorf("exchange token: %w", err)
	}
	if err := t.store.SaveToken(ctx, tok); err != nil {
		return fmt.Errorf("persist token: %w", err)
	}
	return nil
}
