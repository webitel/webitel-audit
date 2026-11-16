package grpc

import (
	"go.uber.org/fx"

	auditv1 "github.com/webitel/webitel-audit/gen/go/audit/v1"
	grpcserver "github.com/webitel/webitel-audit/infra/server/grpc"
)

var Module = fx.Module("grpc_handler",
	fx.Provide(NewScorecardServer),
	fx.Invoke(func(s *grpcserver.Server, scorecards *ScorecardServer) {
		auditv1.RegisterScorecardServiceServer(s, scorecards)
	}),
)
