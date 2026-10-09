package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrWSTicketMissing = errors.New("websocket ticket missing")

// WSTicketStore keeps the original JWT only in a short-lived server-side record.
type WSTicketStore struct{ rdb *redis.Client }

func NewWSTicketStore() *WSTicketStore { return &WSTicketStore{rdb: RDB} }

type wsTicketIdentity struct {
	UserID int64  `json:"user_id"`
	Token  string `json:"token"`
}

func wsTicketKey(ticket string) string {
	hash := sha256.Sum256([]byte(ticket))
	return "ws_ticket:" + hex.EncodeToString(hash[:])
}

func (s *WSTicketStore) Issue(ctx context.Context, userID int64, token string, ttl time.Duration) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(bytes)
	value, err := json.Marshal(wsTicketIdentity{UserID: userID, Token: token})
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, wsTicketKey(ticket), value, ttl).Err(); err != nil {
		return "", err
	}
	return ticket, nil
}

func (s *WSTicketStore) Consume(ctx context.Context, ticket string) (int64, string, error) {
	if len(ticket) != 43 {
		return 0, "", ErrWSTicketMissing
	}
	bytes, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil || len(bytes) != 32 {
		return 0, "", ErrWSTicketMissing
	}
	value, err := s.rdb.GetDel(ctx, wsTicketKey(ticket)).Bytes()
	if errors.Is(err, redis.Nil) {
		return 0, "", ErrWSTicketMissing
	}
	if err != nil {
		return 0, "", err
	}
	var identity wsTicketIdentity
	if err := json.Unmarshal(value, &identity); err != nil || identity.UserID <= 0 || identity.Token == "" {
		return 0, "", ErrWSTicketMissing
	}
	return identity.UserID, identity.Token, nil
}
