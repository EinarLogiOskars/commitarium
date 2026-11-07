package workerhttp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A worker swapped under its name can leave a kept connection to a container
// that refuses the token; after a 401 the client dials again.
func TestARefusedTokenDropsTheKeptConnection(t *testing.T) {
	var calls, dials atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"a valid bearer token is required","retryable":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"provider":"claude_code","windows":[]}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			dials.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, BearerToken: "token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Usage(t.Context()); err == nil {
		t.Fatal("a refused token answered")
	}
	if _, err := client.Usage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dialled %d times, want a new connection after the 401", got)
	}
}
