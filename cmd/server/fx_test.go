package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	healthfx "github.com/webitel/webitel-go-kit/infra/health/fx"

	"github.com/webitel/webitel-audit/config"
	"github.com/webitel/webitel-audit/internal/model"
)

func TestGraphIsValid(t *testing.T) {
	cfg := &config.Config{Service: config.ServiceConfig{Addr: "127.0.0.1:0"}}

	require.NoError(t, fx.ValidateApp(options(cfg)))
}

// TestShutdownOrder checks that the instance leaves Consul while the gRPC server still serves.
func TestShutdownOrder(t *testing.T) {
	var (
		mu           sync.Mutex
		registration struct {
			Name            string
			Address         string
			Port            int
			TaggedAddresses map[string]struct {
				Address string
				Port    int
			}
		}
		registered         bool
		statusAtDeregister healthpb.HealthCheckResponse_ServingStatus
		deregistered       bool
		conn               *grpc.ClientConn
	)

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.URL.Path == "/v1/agent/self":
			_, _ = io.WriteString(w, `{"Config":{"NodeName":"test"}}`)

			return
		case r.URL.Path == "/v1/agent/service/register":
			registered = json.NewDecoder(r.Body).Decode(&registration) == nil
		case strings.HasPrefix(r.URL.Path, "/v1/agent/service/deregister/"):
			deregistered = true

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
			if err == nil {
				statusAtDeregister = resp.GetStatus()
			} else {
				statusAtDeregister = -1
			}
		}

		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(consul.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	cfg := &config.Config{
		Service: config.ServiceConfig{Addr: addr},
	}
	cfg.Consul.Addr = strings.TrimPrefix(consul.URL, "http://")

	app := fxtest.New(t,
		fx.NopLogger,
		fx.Supply(cfg),
		fx.Provide(func() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }),
		healthfx.Module(healthfx.Config{DrainHold: 50 * time.Millisecond}),
		serving(cfg),
		healthfx.Shutdown(),
	)

	app.RequireStart()

	conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	mu.Lock()
	assert.True(t, registered, "registered in Consul on start")
	assert.Equal(t, model.ServiceName, registration.Name)
	assert.Equal(t, host, registration.Address, "the address Consul checks")
	assert.Equal(t, port, strconv.Itoa(registration.Port))
	// the gRPC resolvers find the service by the "grpc" tagged address
	assert.Equal(t, "grpc://"+addr, registration.TaggedAddresses["grpc"].Address)
	assert.Equal(t, port, strconv.Itoa(registration.TaggedAddresses["grpc"].Port))
	mu.Unlock()

	app.RequireStop()

	mu.Lock()
	defer mu.Unlock()

	require.True(t, deregistered, "deregistered from Consul on stop")
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, statusAtDeregister, "server still serving at Deregister")
}
