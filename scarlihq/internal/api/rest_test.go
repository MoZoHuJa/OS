package api

import (
	"fmt"
	"testing"
	"time"
)

// v18.8.2 P2: Unit tests for ReserveWSTicket single-use atomicity
// (was: no tests — the core replay-resistance guarantee was untested)

func TestReserveWSTicketEmpty(t *testing.T) {
	if ReserveWSTicket("") {
		t.Error("expected false for empty ticket")
	}
}

func TestReserveWSTicketUnknownTicket(t *testing.T) {
	if ReserveWSTicket("nonexistent-ticket-12345") {
		t.Error("expected false for unknown ticket")
	}
}

func TestReserveWSTicketSingleUse(t *testing.T) {
	// issueWSTicket is unexported, so we test via the store directly.
	// Manually insert a ticket, then verify Reserve deletes it (single-use).
	ticket := "test-ticket-single-use-" + time.Now().Format("150405.000000")
	wsTicketsMu.Lock()
	wsTickets[ticket] = time.Now().Add(30 * time.Second)
	wsTicketsMu.Unlock()

	// First Reserve must succeed (deletes the ticket)
	if !ReserveWSTicket(ticket) {
		t.Error("first Reserve should succeed for known ticket")
	}

	// Second Reserve must fail (already consumed)
	if ReserveWSTicket(ticket) {
		t.Error("second Reserve should fail — ticket already consumed (single-use broken)")
	}
}

func TestReserveWSTicketExpiredFails(t *testing.T) {
	ticket := "test-ticket-expired-" + time.Now().Format("150405.000000")
	wsTicketsMu.Lock()
	wsTickets[ticket] = time.Now().Add(-1 * time.Second) // expired
	wsTicketsMu.Unlock()

	if ReserveWSTicket(ticket) {
		t.Error("expected false for expired ticket")
	}

	// Verify expired ticket was cleaned up
	wsTicketsMu.Lock()
	_, exists := wsTickets[ticket]
	wsTicketsMu.Unlock()
	if exists {
		t.Error("expired ticket should be deleted after Reserve attempt")
	}
}

func TestReserveWSTicketConcurrentSingleUse(t *testing.T) {
	// Insert one ticket, then have N goroutines race to reserve it.
	// Exactly ONE must succeed (single-use guarantee under concurrency).
	ticket := "test-ticket-concurrent-" + time.Now().Format("150405.000000")
	wsTicketsMu.Lock()
	wsTickets[ticket] = time.Now().Add(30 * time.Second)
	wsTicketsMu.Unlock()

	const N = 20
	results := make(chan bool, N)
	for i := 0; i < N; i++ {
		go func() {
			results <- ReserveWSTicket(ticket)
		}()
	}

	successes := 0
	for i := 0; i < N; i++ {
		if <-results {
			successes++
		}
	}

	if successes != 1 {
		t.Errorf("expected exactly 1 success under concurrency, got %d (single-use atomicity broken)", successes)
	}
}

func TestIssueWSTicketRateLimit(t *testing.T) {
	// v18.8.2: issueWSTicket returns "" when store is at capacity.
	// Fill the store to maxWSTickets, then verify next issue fails.
	wsTicketsMu.Lock()
	// Clear existing tickets
	for k := range wsTickets {
		delete(wsTickets, k)
	}
	// Fill to capacity
	for i := 0; i < maxWSTickets; i++ {
		wsTickets[rateLimitTestTicket(i)] = time.Now().Add(30 * time.Second)
	}
	wsTicketsMu.Unlock()

	ticket, err := issueWSTicket()
	if err == nil {
		t.Error("expected error when store at capacity, got ticket: " + ticket)
	}
	if err != ErrTicketLimit {
		t.Errorf("expected ErrTicketLimit, got %v", err)
	}

	// Cleanup
	wsTicketsMu.Lock()
	for k := range wsTickets {
		delete(wsTickets, k)
	}
	wsTicketsMu.Unlock()
}

func TestIssueWSTicketSuccess(t *testing.T) {
	// Clear store, verify normal issue works
	wsTicketsMu.Lock()
	for k := range wsTickets {
		delete(wsTickets, k)
	}
	wsTicketsMu.Unlock()

	ticket, err := issueWSTicket()
	if err != nil {
		t.Fatalf("issueWSTicket failed: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket")
	}
	if ticket == "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Error("ticket is all-zeros — RNG failure not handled")
	}

	// Verify ticket is in the store and can be reserved
	if !ReserveWSTicket(ticket) {
		t.Error("issued ticket should be reservable")
	}
}

// rateLimitTestTicket generates a unique ticket name for rate limit testing.
func rateLimitTestTicket(i int) string {
	return "rate-limit-test-" + fmt.Sprintf("%04d", i)
}
