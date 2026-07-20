package core

import (
	"context"
	"fmt"

	"github.com/champly/mecha/pkg/agent"
	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// grpcService implements api.CoreServer, routing RPCs to registry instances.
type grpcService struct {
	api.UnimplementedCoreServer
	core *Core
}

// Register handles agentd registration and returns the rendered role config.
func (s *grpcService) Register(ctx context.Context, req *api.RegisterRequest) (*api.RegisterResponse, error) {
	inst := s.core.registry.get(req.Id)
	if inst == nil {
		return nil, fmt.Errorf("unknown instance %q", req.Id)
	}
	inst.markRegistered()

	role, ok := s.core.findRole(inst.role)
	if !ok {
		return nil, fmt.Errorf("unknown role %q", inst.role)
	}

	return &api.RegisterResponse{
		Workspace: s.core.workspace,
		Prompt: agent.RenderPrompt(s.core.workspace, config.Runtime{
			MechaBinary: s.core.mechaBinary,
			Addr:        s.core.addr,
		}, role, s.core.profileRoles()),
		RoleName:    inst.role,
		Agent:       api.AgentConfigFromNative(role.Agent),
		MechaBinary: s.core.mechaBinary,
	}, nil
}

// ReportStatus handles agentd status reports: started → ready, exited → unhealthy.
func (s *grpcService) ReportStatus(ctx context.Context, req *api.StatusRequest) (*emptypb.Empty, error) {
	inst := s.core.registry.get(req.Id)
	if inst == nil {
		return &emptypb.Empty{}, nil
	}

	switch req.Status {
	case api.StatusStarted:
		inst.markStarted()
		s.core.logger.Info("agent started", "role", inst.role, "id", inst.id)
	case api.StatusExited:
		inst.markExited()
		s.core.logger.Info("agent exited", "role", inst.role, "id", inst.id)
	}
	return &emptypb.Empty{}, nil
}

// Ask dispatches a task to the role's specialist.
func (s *grpcService) Ask(ctx context.Context, req *api.AskRequest) (*api.AskResponse, error) {
	inst, err := s.core.ensureSpecialist(ctx, req.Role)
	if err != nil {
		return nil, err
	}
	return inst.execute(ctx, req.Task)
}

// TaskChannel handles the agentd bidi stream: attach it, then deliver task
// results to waiting Asks until the stream breaks.
func (s *grpcService) TaskChannel(stream grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]) error {
	id := api.GetInstanceID(stream.Context())
	if id == "" {
		return fmt.Errorf("missing instance ID")
	}
	inst := s.core.registry.get(id)
	if inst == nil {
		return fmt.Errorf("unknown instance %q for TaskChannel", id)
	}

	inst.attach(stream)
	defer inst.detach()

	for {
		result, err := stream.Recv()
		if err != nil {
			return err
		}
		inst.deliverResult(&api.AskResponse{
			Id:      result.Id,
			Success: result.Success,
			Result:  result.Result,
		})
	}
}
