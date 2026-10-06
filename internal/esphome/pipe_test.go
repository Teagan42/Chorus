package esphome

import (
	"net"
	"testing"
)

// newPipe returns an in-memory connection pair. net.Pipe is synchronous and
// unbuffered, which is what we want: it makes a missing read deadlock the test
// instead of passing by accident.
func newPipe(t *testing.T) (client, server net.Conn) {
	t.Helper()
	client, server = net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}
