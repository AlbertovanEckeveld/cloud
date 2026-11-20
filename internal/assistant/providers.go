package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Provider is an OpenAI-compatible endpoint as administrators see it. The
// API key is never returned, only whether there is one.
type Provider struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	BaseURL   string     `json:"base_url"`
	HasKey    bool       `json:"has_key"`
	External  bool       `json:"external"`
	ConsentAt *time.Time `json:"consent_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ProviderInput is what an administrator sets. Key nil keeps the stored
// key, "" removes it. ConsentExternal must be true to save an external
// provider: content will go there.
type ProviderInput struct {
	Name            string
	BaseURL         string
	Key             *string
	ConsentExternal bool
}

// Settings say which provider and model answer and which make embeddings.
type Settings struct {
	ChatProvider  *uuid.UUID `json:"chat_provider"`
	ChatModel     string     `json:"chat_model"`
	EmbedProvider *uuid.UUID `json:"embed_provider"`
	EmbedModel    string     `json:"embed_model"`
}

func keyAD(id uuid.UUID) []byte { return []byte("assistant:provider:" + id.String()) }

func validName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > 60 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", &InputError{"Geef de aanbieder een naam van 1 tot 60 tekens."}
	}
	return s, nil
}

func validKey(k string) error {
	if len(k) > 500 || strings.IndexFunc(k, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return &InputError{"Deze API-sleutel klopt niet: geen spaties, hoogstens 500 tekens."}
	}
	return nil
}

func (s *Service) scanProviders(rows pgx.Rows) ([]Provider, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Provider, error) {
		var p Provider
		var key []byte
		err := row.Scan(&p.ID, &p.Name, &p.BaseURL, &key, &p.External, &p.ConsentAt, &p.UpdatedAt)
		p.HasKey = key != nil
		p.UpdatedAt = p.UpdatedAt.UTC()
		return p, err
	})
}

const providerColumns = `id, name, base_url, api_key, external, consent_at, updated_at`

// Providers lists the configured providers.
func (s *Service) Providers(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.Query(ctx, `SELECT `+providerColumns+` FROM assistant.providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out, err := s.scanProviders(rows)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	return out, nil
}

// SaveProvider adds (id nil) or changes a provider. Whether it is external
// is decided here from its address, not by the administrator.
func (s *Service) SaveProvider(ctx context.Context, actor uuid.UUID, id *uuid.UUID, in ProviderInput) (Provider, error) {
	name, err := validName(in.Name)
	if err != nil {
		return Provider{}, err
	}
	u, err := parseBaseURL(in.BaseURL)
	if err != nil {
		return Provider{}, err
	}
	external, err := s.guard.isExternal(ctx, u.Hostname())
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return Provider{}, &InputError{"Dit adres is niet toegestaan."}
		}
		return Provider{}, &InputError{"Dit adres is niet te vinden."}
	}
	if external && u.Scheme != "https" {
		return Provider{}, &InputError{"Een aanbieder buiten je netwerk moet https gebruiken: anders gaan je sleutel en je bestanden onversleuteld over het internet."}
	}
	if external && !in.ConsentExternal {
		return Provider{}, &InputError{"Dit is een externe aanbieder. Bevestig dat de inhoud van bestanden naar " + u.Hostname() + " mag gaan."}
	}
	if in.Key != nil && *in.Key != "" {
		if err := validKey(*in.Key); err != nil {
			return Provider{}, err
		}
	}
	now := s.now()
	pid := newID()
	if id != nil {
		pid = *id
	}
	var sealed []byte
	if in.Key != nil && *in.Key != "" {
		sealed = s.sealer.Seal([]byte(*in.Key), keyAD(pid))
	}
	var consentBy *uuid.UUID
	var consentAt *time.Time
	if external {
		consentBy, consentAt = &actor, &now
	}
	if id == nil {
		_, err = s.db.Exec(ctx, `
			INSERT INTO assistant.providers (id, name, base_url, api_key, external, consent_by, consent_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, pid, name, u.String(), sealed, external, consentBy, consentAt, now)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `
			UPDATE assistant.providers
			SET name = $2, base_url = $3, external = $4, consent_by = $5, consent_at = $6, updated_at = $7,
			    api_key = CASE WHEN $8 THEN $9 ELSE api_key END
			WHERE id = $1`, pid, name, u.String(), external, consentBy, consentAt, now, in.Key != nil, sealed)
		if err == nil && tag.RowsAffected() == 0 {
			return Provider{}, ErrNotFound
		}
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return Provider{}, &InputError{"Er bestaat al een aanbieder met deze naam."}
	}
	if err != nil {
		return Provider{}, fmt.Errorf("save provider: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT `+providerColumns+` FROM assistant.providers WHERE id = $1`, pid)
	if err != nil {
		return Provider{}, fmt.Errorf("load provider: %w", err)
	}
	ps, err := s.scanProviders(rows)
	if err != nil || len(ps) != 1 {
		return Provider{}, fmt.Errorf("load provider: %w", err)
	}
	return ps[0], nil
}

// DeleteProvider removes a provider; settings that used it are cleared.
func (s *Service) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM assistant.providers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// clientFor opens a provider for use.
func (s *Service) clientFor(ctx context.Context, q dbtx, id uuid.UUID) (*client, Provider, error) {
	var p Provider
	var key []byte
	err := q.QueryRow(ctx, `SELECT `+providerColumns+` FROM assistant.providers WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.BaseURL, &key, &p.External, &p.ConsentAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Provider{}, ErrNotFound
	}
	if err != nil {
		return nil, Provider{}, fmt.Errorf("load provider: %w", err)
	}
	u, err := parseBaseURL(p.BaseURL)
	if err != nil {
		return nil, Provider{}, err
	}
	var plain string
	if key != nil {
		b, err := s.sealer.Open(key, keyAD(p.ID))
		if err != nil {
			return nil, Provider{}, fmt.Errorf("open provider key: %w", err)
		}
		plain = string(b)
	}
	return newClient(s.guard, u, plain, p.External), p, nil
}

// TestProvider connects and returns the models the provider offers.
func (s *Service) TestProvider(ctx context.Context, id uuid.UUID) ([]string, error) {
	c, _, err := s.clientFor(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return c.Models(ctx)
}

// GetSettings returns the current choice of providers and models.
func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	var st Settings
	var chat, embed *string
	err := s.db.QueryRow(ctx, `SELECT chat_provider, chat_model, embed_provider, embed_model FROM assistant.settings`).
		Scan(&st.ChatProvider, &chat, &st.EmbedProvider, &embed)
	if err != nil {
		return Settings{}, fmt.Errorf("load settings: %w", err)
	}
	if chat != nil {
		st.ChatModel = *chat
	}
	if embed != nil {
		st.EmbedModel = *embed
	}
	return st, nil
}

// SaveSettings chooses providers and models. A new embedding model means
// re-indexing, which starts at once.
func (s *Service) SaveSettings(ctx context.Context, st Settings) error {
	clean := func(m string) (*string, error) {
		m = strings.TrimSpace(m)
		if m == "" {
			return nil, nil
		}
		if len(m) > 200 || strings.IndexFunc(m, unicode.IsControl) >= 0 {
			return nil, &InputError{"Deze modelnaam klopt niet."}
		}
		return &m, nil
	}
	chat, err := clean(st.ChatModel)
	if err != nil {
		return err
	}
	embed, err := clean(st.EmbedModel)
	if err != nil {
		return err
	}
	if (st.ChatProvider == nil) != (chat == nil) || (st.EmbedProvider == nil) != (embed == nil) {
		return &InputError{"Kies bij elke aanbieder ook een model."}
	}
	// Catch a wrong choice now rather than in every search: the models must
	// exist, and the embedding model must actually make embeddings.
	if st.ChatProvider != nil {
		if err := s.checkModel(ctx, *st.ChatProvider, *chat, false); err != nil {
			return err
		}
	}
	if st.EmbedProvider != nil {
		if err := s.checkModel(ctx, *st.EmbedProvider, *embed, true); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(ctx, `
		UPDATE assistant.settings SET chat_provider = $1, chat_model = $2, embed_provider = $3, embed_model = $4, updated_at = $5`,
		st.ChatProvider, chat, st.EmbedProvider, embed, s.now())
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23503" {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	s.Kick()
	return nil
}

// embedModelID names the embedding model in the index: provider and model,
// so the same model name at another provider is never mixed up.
func embedModelID(provider uuid.UUID, model string) string {
	return provider.String() + "/" + model
}

// embedder opens the configured embedding model, or ErrNotConfigured.
func (s *Service) embedder(ctx context.Context) (*client, string, string, error) {
	st, err := s.GetSettings(ctx)
	if err != nil {
		return nil, "", "", err
	}
	if st.EmbedProvider == nil || st.EmbedModel == "" {
		return nil, "", "", ErrNotConfigured
	}
	c, _, err := s.clientFor(ctx, s.db, *st.EmbedProvider)
	if err != nil {
		return nil, "", "", err
	}
	return c, st.EmbedModel, embedModelID(*st.EmbedProvider, st.EmbedModel), nil
}

// checkModel verifies that a provider offers model and, for embeddings,
// that it really makes them. The provider's own message stays in the log.
func (s *Service) checkModel(ctx context.Context, provider uuid.UUID, model string, embeddings bool) error {
	c, p, err := s.clientFor(ctx, s.db, provider)
	if err != nil {
		return err
	}
	models, err := c.Models(ctx)
	if err != nil {
		return err
	}
	// Some providers name models "models/x"; accept either spelling.
	known := false
	for _, m := range models {
		if m == model || strings.TrimPrefix(m, "models/") == strings.TrimPrefix(model, "models/") {
			known = true
			break
		}
	}
	if !known {
		return &InputError{"Het model \"" + model + "\" staat niet in de lijst van " + p.Name + "."}
	}
	if !embeddings {
		return nil
	}
	if _, err := c.Embed(ctx, model, []string{"proef"}); err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) && (pe.Kind == "incompatible" || pe.Kind == "failed") {
			s.log.Warn("assistant embedding model refused", "model", model, "error", pe.Detail())
			return &InputError{"\"" + model + "\" maakt geen embeddings bij " + p.Name + ". Kies een embedding-model, meestal met \"embed\" in de naam."}
		}
		return err
	}
	return nil
}
