package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// logTicketTTL is deliberately longer than the original in-memory ticket so a
// long-running build's EventSource can reconnect after a transient network
// drop. Tickets are scoped to one run and are not reusable for another run.
const logTicketTTL = 10 * time.Minute

type logTicketPayload struct {
	Version int       `json:"v"`
	RunID   uuid.UUID `json:"runId"`
	Expires int64     `json:"exp"`
	Nonce   string    `json:"nonce"`
}

func (s *Server) logTicketKey() []byte {
	sum := sha256.Sum256([]byte("scheduler-log-ticket-v1:" + s.cfg.DevToken))
	return sum[:]
}

func (s *Server) signLogTicketAt(runID uuid.UUID, now time.Time) (string, int64, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", 0, err
	}
	payload := logTicketPayload{
		Version: 1,
		RunID:   runID,
		Expires: now.Add(logTicketTTL).Unix(),
		Nonce:   base64.RawURLEncoding.EncodeToString(nonce),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.logTicketKey())
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), int64(logTicketTTL / time.Second), nil
}

func (s *Server) newLogTicket(runID uuid.UUID) (string, int64) {
	ticket, expiresIn, err := s.signLogTicketAt(runID, time.Now())
	if err != nil {
		return "", 0
	}
	return ticket, expiresIn
}

func (s *Server) validateLogTicket(runID uuid.UUID, ticket string) bool {
	return s.validateLogTicketAt(runID, ticket, time.Now())
}

func (s *Server) validateLogTicketAt(runID uuid.UUID, ticket string, now time.Time) bool {
	if ticket == "" {
		return false
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, s.logTicketKey())
	_, _ = mac.Write([]byte(parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var payload logTicketPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if payload.Version != 1 || payload.RunID != runID || payload.Expires <= now.Unix() {
		return false
	}
	return true
}

// ticketExpiresAt is exposed for tests without parsing the signature.
func ticketExpiresAt(ticket string) (time.Time, bool) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return time.Time{}, false
	}
	var payload logTicketPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return time.Time{}, false
	}
	return time.Unix(payload.Expires, 0), true
}
