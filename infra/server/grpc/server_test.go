package grpcserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/webitel/webitel-go-kit/pkg/errors"

	"github.com/webitel/webitel-audit/config"
	"github.com/webitel/webitel-audit/internal/model"
)

func testConfig() *config.Config {
	return &config.Config{Service: config.ServiceConfig{Addr: "127.0.0.1:0"}}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// unary builds a handler that runs the interceptor chain and then fn.
func unary(method string, fn func() error) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(_ any, ctx context.Context, dec func(any) error, i grpc.UnaryServerInterceptor) (any, error) {
		in := new(emptypb.Empty)
		if err := dec(in); err != nil {
			return nil, err
		}

		h := func(context.Context, any) (any, error) {
			if err := fn(); err != nil {
				return nil, err
			}

			return in, nil
		}

		return i(ctx, in, &grpc.UnaryServerInfo{FullMethod: method}, h)
	}
}

// startServer returns a started server and a client connected to it.
func startServer(t *testing.T) (*Server, *fxtest.Lifecycle, *grpc.ClientConn) {
	t.Helper()

	lc := fxtest.NewLifecycle(t)
	s, err := New(testConfig(), discardLog(), lc)
	require.NoError(t, err)

	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Svc",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Panic", Handler: unary("/test.Svc/Panic", func() error { panic("boom") })},
			{MethodName: "NotFound", Handler: unary("/test.Svc/NotFound", func() error { return errors.NotFound("no such entity") })},
			{MethodName: "Echo", Handler: unary("/test.Svc/Echo", func() error { return nil })},
		},
	}, struct{}{})

	lc.RequireStart()

	conn, err := grpc.NewClient(s.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return s, lc, conn
}

func TestHealthAndReflection(t *testing.T) {
	_, _, conn := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, svc := range []string{"", model.ServiceName} {
		resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: svc})
		require.NoError(t, err)
		assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus(), svc)
	}

	stream, err := reflectionpb.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}))
	resp, err := stream.Recv()
	require.NoError(t, err)

	services := resp.GetListServicesResponse().GetService()
	names := make([]string, 0, len(services))

	for _, svc := range services {
		names = append(names, svc.GetName())
	}

	assert.Contains(t, names, "grpc.health.v1.Health")
}

func TestNotServingBeforeStart(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	s, err := New(testConfig(), discardLog(), lc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.listener.Close() })

	resp, err := healthCheck(s)
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, resp.GetStatus())
}

func healthCheck(s *Server) (*healthpb.HealthCheckResponse, error) {
	return s.health.Check(context.Background(), &healthpb.HealthCheckRequest{})
}

// gatewayError decodes the body the gateway reads from the status message.
func gatewayError(t *testing.T, err error) (id string, code int, detail string) {
	t.Helper()

	var body struct {
		ID     string `json:"id"`
		Code   int    `json:"code"`
		Detail string `json:"detail"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(status.Convert(err).Message()), &body), "status message is the gateway JSON")
	assert.Equal(t, http.StatusText(body.Code), body.Status)

	return body.ID, body.Code, body.Detail
}

func TestPanicIsRecovered(t *testing.T) {
	_, _, conn := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := conn.Invoke(ctx, "/test.Svc/Panic", &emptypb.Empty{}, &emptypb.Empty{})

	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	id, code, detail := gatewayError(t, err)
	assert.Equal(t, "api.process.internal", id)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.NotContains(t, detail, "boom", "the panic value stays in the log")
	// the server survives the panic
	require.NoError(t, conn.Invoke(ctx, "/test.Svc/Echo", &emptypb.Empty{}, &emptypb.Empty{}))
}

func TestHandlerErrorIsGatewayJSON(t *testing.T) {
	_, _, conn := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := conn.Invoke(ctx, "/test.Svc/NotFound", &emptypb.Empty{}, &emptypb.Empty{})

	assert.Equal(t, codes.NotFound, status.Code(err))
	id, code, detail := gatewayError(t, err)
	assert.Equal(t, "api.process.not_found", id)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, detail, "no such entity")
}

func TestStopMarksNotServingAndForcesStuckWatch(t *testing.T) {
	s, lc, conn := startServer(t)
	s.stopTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watch, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	first, err := watch.Recv()
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, first.GetStatus())

	// the open Watch holds GracefulStop, so the stop falls back to Stop without failing the shutdown
	require.NoError(t, lc.Stop(ctx))

	next, err := watch.Recv()
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, next.GetStatus())
}

func TestNewFailsOnBrokenTLS(t *testing.T) {
	cfg := testConfig()
	cfg.Service.Connection.VerifyCerts = true
	cfg.Service.Connection.Cert = "missing.pem"
	cfg.Service.Connection.Key = "missing.key"

	_, err := New(cfg, discardLog(), fxtest.NewLifecycle(t))

	require.Error(t, err)
}
