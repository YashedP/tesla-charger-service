package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	_ "modernc.org/sqlite"
)

var ErrTokenNotFound = errors.New("oauth token not found")

const sqliteDriverName = "sqlite"

//go:embed sql/migrate.sql
var migrateSQL string

//go:embed sql/upsert_token.sql
var upsertTokenSQL string

//go:embed sql/select_token.sql
var selectTokenSQL string

type StringCipher interface {
	EncryptString(plaintext string) (string, error)
	DecryptString(encoded string) (string, error)
}

type SQLiteTokenStore struct {
	db     *sql.DB
	cipher StringCipher
}

func NewSQLiteTokenStore(dbPath string, cipher StringCipher) (*SQLiteTokenStore, error) {
	db, err := sql.Open(sqliteDriverName, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	store := &SQLiteTokenStore{db: db, cipher: cipher}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *SQLiteTokenStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, migrateSQL); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	return nil
}

func (s *SQLiteTokenStore) SaveToken(ctx context.Context, token *oauth2.Token) error {
	if token == nil {
		return errors.New("token is nil")
	}

	access, err := s.cipher.EncryptString(token.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refresh, err := s.cipher.EncryptString(token.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	typeEnc, err := s.cipher.EncryptString(token.TokenType)
	if err != nil {
		return fmt.Errorf("encrypt token type: %w", err)
	}

	expiryUnix := int64(0)
	if !token.Expiry.IsZero() {
		expiryUnix = token.Expiry.Unix()
	}

	if _, err := s.db.ExecContext(ctx, upsertTokenSQL,
		access,
		refresh,
		typeEnc,
		expiryUnix,
		time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("save oauth token: %w", err)
	}

	return nil
}

func (s *SQLiteTokenStore) LoadToken(ctx context.Context) (*oauth2.Token, error) {
	var accessEnc, refreshEnc, typeEnc string
	var expiryUnix int64

	if err := s.db.QueryRowContext(ctx, selectTokenSQL).Scan(&accessEnc, &refreshEnc, &typeEnc, &expiryUnix); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("load oauth token: %w", err)
	}

	access, err := s.cipher.DecryptString(accessEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	refresh, err := s.cipher.DecryptString(refreshEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	tokType, err := s.cipher.DecryptString(typeEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt token type: %w", err)
	}

	token := &oauth2.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    tokType,
	}
	if expiryUnix > 0 {
		token.Expiry = time.Unix(expiryUnix, 0).UTC()
	}

	return token, nil
}

func (s *SQLiteTokenStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
