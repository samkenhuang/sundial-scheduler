package api

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/config"
)

func testTicketServer() *Server {
	return &Server{cfg: config.Config{DevToken: "dev-token"}}
}

func TestLogTicketRoundTripAndRejectsTampering(t *testing.T) {
	s := testTicketServer()
	runID := uuid.New()
	now := time.Now()
	ticket, _, err := s.signLogTicketAt(runID, now)
	if err != nil {
		t.Fatalf("sign ticket: %v", err)
	}
	if !s.validateLogTicketAt(runID, ticket, now) {
		t.Fatal("valid ticket rejected")
	}
	if s.validateLogTicketAt(uuid.New(), ticket, now) {
		t.Fatal("ticket accepted for another run")
	}
	if s.validateLogTicketAt(runID, ticket+"x", now) {
		t.Fatal("tampered signature accepted")
	}
	parts := strings.Split(ticket, ".")
	parts[0] = parts[0][:len(parts[0])-2] + "AA"
	if s.validateLogTicketAt(runID, strings.Join(parts, "."), now) {
		t.Fatal("tampered payload accepted")
	}
	if s.validateLogTicketAt(runID, ticket, now.Add(logTicketTTL+time.Second)) {
		t.Fatal("expired ticket accepted")
	}
	if s.validateLogTicketAt(runID, "", now) {
		t.Fatal("empty ticket accepted")
	}
}

func TestLogTicketWorksAcrossServerInstances(t *testing.T) {
	a := testTicketServer()
	b := testTicketServer()
	runID := uuid.New()
	ticket, _, err := a.signLogTicketAt(runID, time.Now())
	if err != nil {
		t.Fatalf("sign ticket: %v", err)
	}
	if !b.validateLogTicket(runID, ticket) {
		t.Fatal("ticket issued by one instance rejected by another with the same dev token")
	}
	other := &Server{cfg: config.Config{DevToken: "other-token"}}
	if other.validateLogTicket(runID, ticket) {
		t.Fatal("ticket accepted by a server with a different token")
	}
}

func TestLogTicketExpiryMetadata(t *testing.T) {
	s := testTicketServer()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ticket, expiresIn, err := s.signLogTicketAt(uuid.New(), now)
	if err != nil {
		t.Fatalf("sign ticket: %v", err)
	}
	if expiresIn != int64(logTicketTTL/time.Second) {
		t.Fatalf("expiresIn=%d", expiresIn)
	}
	expires, ok := ticketExpiresAt(ticket)
	if !ok || !expires.Equal(now.Add(logTicketTTL)) {
		t.Fatalf("expiry=%v ok=%v", expires, ok)
	}
}
