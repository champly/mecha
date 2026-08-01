package api

import (
	"context"
	"fmt"
	"time"

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

	// TaskTimeout bounds one task on both sides of the stream: Core stops
	// waiting and agentd stops waiting for the hook result, so a stuck agent
	// cannot wedge the role forever.
	TaskTimeout = 30 * time.Minute
)

// AgentConfigFromNative converts config.AgentConfig to the proto type.
func AgentConfigFromNative(cfg config.AgentConfig) (*AgentConfig, error) {
	params, err := paramsToStruct(cfg.Params)
	if err != nil {
		return nil, fmt.Errorf("api: agent params: %w", err)
	}
	return &AgentConfig{
		Type:   cfg.Type,
		Binary: cfg.Binary,
		Model:  cfg.Model,
		Params: params,
		Envs:   cfg.Envs,
	}, nil
}

func paramsToStruct(m map[string]any) (*structpb.Struct, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(m)
}
