package mailer

import (
	"context"
	"testing"
	"time"
)

// TestDialHostWaitsBetweenAttempts asserts that the reconnect attempts are
// spread over time rather than fired back to back.
//
// MaxReconnectAttempts is 10, but dialHost looped with no delay at all, so all
// ten dials completed within microseconds of each other. Against a server that
// is briefly refusing connections - a rate limited relay, a restarting MTA, a
// transient network fault - ten immediate attempts are no more likely to
// succeed than one, and the entire batch is errored out. This matters most
// against an external SMTP provider, which is the common deployment.
func TestDialHostWaitsBetweenAttempts(t *testing.T) {
	md := newMockDialer()
	md.setDial(md.unreachableDial)

	start := time.Now()
	_, err := dialHost(context.Background(), md)
	elapsed := time.Since(start)

	if _, ok := err.(*ErrMaxConnectAttempts); !ok {
		t.Fatalf("expected ErrMaxConnectAttempts, got %v", err)
	}
	if md.dialCount != MaxReconnectAttempts {
		t.Fatalf("expected %d dials, got %d", MaxReconnectAttempts, md.dialCount)
	}
	floor := 50 * time.Millisecond
	if elapsed < floor {
		t.Fatalf("dialHost made %d attempts in %s, so nothing waits between them and "+
			"MaxReconnectAttempts is effectively a single attempt; expected at least %s",
			md.dialCount, elapsed, floor)
	}
}

// TestDialHostReportsACancelledContext asserts dialHost never reports success
// without returning a Sender.
//
// It returned a nil Sender with a nil error once the context was done. sendMail
// checks only the error and then defers sender.Close(), which panics on a nil
// Sender.
func TestDialHostReportsACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	md := newMockDialer()
	md.setDial(md.unreachableDial)
	sender, err := dialHost(ctx, md)

	if err == nil {
		t.Fatal("dialHost returned a nil error for a cancelled context, so sendMail " +
			"defers Close on a nil Sender and panics")
	}
	if sender != nil {
		t.Fatalf("expected no Sender alongside the error, got %v", sender)
	}
}
