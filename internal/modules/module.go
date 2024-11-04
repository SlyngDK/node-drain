package modules

import (
	"context"
)

type KubernetesStateful interface {
	Name() string
	IsSupported(ctx context.Context) (bool, error)
	IsHealthy(ctx context.Context) (bool, error)
	IsDrainOk(ctx context.Context, nodeName string) (bool, error)
	PreDrain(ctx context.Context, nodeName string) error
	PostDrain(ctx context.Context, nodeName string) error
}
