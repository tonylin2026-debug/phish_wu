package mailer

import (
	"os"
	"testing"
	"time"
)

// TestMain shortens the reconnect delay for the whole package. Several tests
// exhaust MaxReconnectAttempts against an unreachable host, and the production
// default would add ten seconds to each of them. The delay still has to be long
// enough for TestDialHostWaitsBetweenAttempts to observe it.
func TestMain(m *testing.M) {
	ReconnectDelay = 10 * time.Millisecond
	os.Exit(m.Run())
}
