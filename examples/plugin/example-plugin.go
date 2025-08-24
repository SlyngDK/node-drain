package main

import (
	"context"

	"github.com/hashicorp/go-hclog"
	"github.com/slyngdk/node-drain/api/plugins"
)

type ExampleDrainPlugin struct {
	logger hclog.Logger
}

func (e *ExampleDrainPlugin) Init(
	ctx context.Context,
	logger hclog.Logger,
	settings plugins.DrainPluginSettings) (plugins.DrainPluginInfo, error) {
	e.logger = logger
	e.logger.Debug("Init()")

	return plugins.DrainPluginInfo{
		ID: "example-plugin",
	}, nil
}

func (e *ExampleDrainPlugin) IsSupported(ctx context.Context) (bool, error) {
	e.logger.Debug("IsSupported()")
	return true, nil
}

func (e *ExampleDrainPlugin) IsHealthy(ctx context.Context) (bool, error) {
	e.logger.Debug("IsHealthy()")
	return true, nil
}

func (e *ExampleDrainPlugin) IsDrainOk(ctx context.Context, nodeName string) (bool, error) {
	e.logger.Debug("IsDrainOk()")
	return true, nil
}

func (e *ExampleDrainPlugin) PreDrain(ctx context.Context, nodeName string) error {
	e.logger.Debug("PreDrain()")
	return nil
}

func (e *ExampleDrainPlugin) PostDrain(ctx context.Context, nodeName string) error {
	e.logger.Debug("PostDrain()")
	return nil
}

func main() {
	plugins.Serve(&ExampleDrainPlugin{})
}
