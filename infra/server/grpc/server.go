package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	mwinterceptors "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors"
	authmw "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	protovalidatemw "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/selector"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	kithealth "github.com/webitel/webitel-go-kit/infra/health"
	kitgrpc "github.com/webitel/webitel-go-kit/infra/transport/gRPC"
	"github.com/webitel/webitel-go-kit/infra/webitel/auth"
	kiterrors "github.com/webitel/webitel-go-kit/pkg/errors"
	"github.com/webitel/webitel-go-kit/pkg/interceptors"

	"github.com/webitel/webitel-audit/config"
	"github.com/webitel/webitel-audit/internal/model"
)

// stopTimeout bounds GracefulStop; open health Watch streams would otherwise hold it forever.
const stopTimeout = 10 * time.Second

// Module takes the authorizer, so fx builds the auth client first and closes it after GracefulStop.
var Module = fx.Module("grpc_server",
	fx.Provide(func(cfg *config.Config, log *slog.Logger, lc fx.Lifecycle, a *auth.Authorizer) (*Server, error) {
		return New(cfg, log, lc, a.Authorize)
	}),
	fx.Invoke(registerHealth),
)

type Server struct {
	*grpc.Server

	listener    net.Listener
	health      *health.Server
	log         *slog.Logger
	stopTimeout time.Duration
}

// New builds the server and binds its start and stop to fx. Everything that can fail runs before Listen.
func New(cfg *config.Config, log *slog.Logger, lc fx.Lifecycle, authorize authmw.AuthFunc) (*Server, error) {
	creds, err := kitgrpc.ServerCredentials(cfg.Service.Connection)
	if err != nil {
		return nil, fmt.Errorf("grpc server: %w", err)
	}

	if !cfg.Service.Connection.VerifyCerts {
		log.Warn("gRPC server runs without TLS (service.conn.verify_certs=false)")
	}

	validator, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("grpc server: protovalidate: %w", err)
	}

	// go-kit pkg/interceptors has no stream error interceptor. should add StreamServerErrorInterceptor
	authorizeStream := func(ctx context.Context) (context.Context, error) {
		ctx, err := authorize(ctx)
		if err != nil {
			return nil, status.Error(kiterrors.Code(err), err.Error())
		}

		return ctx, nil
	}

	srv := grpc.NewServer(
		grpc.Creds(creds),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		// The error interceptor is outermost so it formats what recovery and the inner ones return.
		grpc.ChainUnaryInterceptor(
			interceptors.UnaryServerErrorInterceptor(),
			interceptors.RecoveryUnaryServerInterceptor(log),
			selector.UnaryServerInterceptor(authmw.UnaryServerInterceptor(authorize), selector.MatchFunc(needsAuth)),
			protovalidatemw.UnaryServerInterceptor(validator),
		),
		grpc.ChainStreamInterceptor(
			selector.StreamServerInterceptor(authmw.StreamServerInterceptor(authorizeStream), selector.MatchFunc(needsAuth)),
		),
	)

	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	setStatus(hs, healthpb.HealthCheckResponse_NOT_SERVING)
	reflection.Register(srv)

	ln, err := net.Listen("tcp", cfg.Service.Addr)
	if err != nil {
		return nil, fmt.Errorf("grpc server: listen on %s: %w", cfg.Service.Addr, err)
	}

	s := &Server{Server: srv, listener: ln, health: hs, log: log, stopTimeout: stopTimeout}
	lc.Append(fx.Hook{OnStart: s.start, OnStop: s.stop})

	return s, nil
}

func (s *Server) start(context.Context) error {
	go func() {
		if err := s.Serve(s.listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			s.log.Error("gRPC server stopped unexpectedly", "error", err)
		}
	}()

	setStatus(s.health, healthpb.HealthCheckResponse_SERVING)
	s.log.Info("gRPC server started", "address", s.listener.Addr().String())

	return nil
}

func (s *Server) stop(ctx context.Context) error {
	s.health.Shutdown()

	done := make(chan struct{})

	go func() {
		s.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info("gRPC server stopped")

		return nil
	case <-time.After(s.stopTimeout):
		// A stuck Watch or reflection stream is expected on shutdown, so the forced stop is not a failure.
		s.Stop()
		s.log.Warn("gRPC graceful stop timed out, forced", "timeout", s.stopTimeout.String())

		return nil
	case <-ctx.Done():
		s.Stop()

		return fmt.Errorf("grpc server: graceful stop: %w", ctx.Err())
	}
}

// needsAuth leaves the health checks and reflection open.
func needsAuth(_ context.Context, c mwinterceptors.CallMeta) bool {
	return c.Service != healthpb.Health_ServiceDesc.ServiceName && !strings.HasPrefix(c.Service, "grpc.reflection.")
}

// registerHealth makes the gRPC listener a critical readiness check.
func registerHealth(s *Server, h *kithealth.Registry) {
	h.Critical(kithealth.SystemGRPC, kithealth.ListenerCheck(s.listener))
}

func setStatus(hs *health.Server, st healthpb.HealthCheckResponse_ServingStatus) {
	hs.SetServingStatus("", st)
	hs.SetServingStatus(model.ServiceName, st)
}
