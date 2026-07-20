package api

import (
	"context"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/champly/mecha/pkg/config"
)

const metaInstanceID = "instance-id"

// NewContextWithID returns a context with the instance ID as gRPC metadata.
func NewContextWithID(ctx context.Context, id string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(metaInstanceID, id))
}

// GetInstanceID extracts the instance ID from incoming gRPC metadata.
func GetInstanceID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	id := md.Get(metaInstanceID)
	if len(id) == 0 {
		return ""
	}
	return id[0]
}

const (
	StatusStarted = "started"
	StatusExited  = "exited"
)

// AgentConfigFromNative converts config.AgentConfig to the proto type.
func AgentConfigFromNative(cfg config.AgentConfig) *AgentConfig {
	return &AgentConfig{
		Type:   cfg.Type,
		Binary: cfg.Binary,
		Model:  cfg.Model,
		Params: paramsToStruct(cfg.Params),
		Envs:   cfg.Envs,
	}
}

func paramsToStruct(m map[string]any) *structpb.Struct {
	if len(m) == 0 {
		return nil
	}
	s, _ := structpb.NewStruct(m)
	return s
}
