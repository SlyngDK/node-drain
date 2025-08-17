package plugins

import (
	"context"

	"github.com/hashicorp/go-hclog"
	proto "github.com/slyngdk/node-drain/api/plugins/proto/v1"
)

var _ DrainPlugin = &DrainClient{}

type DrainClient struct{ client proto.DrainServiceClient }

func (c DrainClient) Init(ctx context.Context, logger hclog.Logger, settings DrainPluginSettings) (DrainPluginInfo, error) {
	resp, err := c.client.Init(ctx, &proto.InitRequest{})
	if err != nil {
		return DrainPluginInfo{}, err
	}

	return DrainPluginInfo{
		ID: resp.Id,
	}, nil
}

func (c DrainClient) IsSupported(ctx context.Context) (bool, error) {
	resp, err := c.client.IsSupported(ctx, &proto.IsSupportedRequest{})
	if err != nil {
		return false, err
	}
	return resp.Supported, nil
}

func (c DrainClient) IsHealthy(ctx context.Context) (bool, error) {
	resp, err := c.client.IsHealthy(ctx, &proto.IsHealthyRequest{})
	if err != nil {
		return false, err
	}
	return resp.Healthy, nil
}

func (c DrainClient) IsDrainOk(ctx context.Context, nodeName string) (bool, error) {
	resp, err := c.client.IsDrainOk(ctx, &proto.IsDrainOkRequest{NodeName: nodeName})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

func (c DrainClient) PreDrain(ctx context.Context, nodeName string) error {
	_, err := c.client.PreDrain(ctx, &proto.PreDrainRequest{NodeName: nodeName})
	return err
}

func (c DrainClient) PostDrain(ctx context.Context, nodeName string) error {
	_, err := c.client.PostDrain(ctx, &proto.PostDrainRequest{NodeName: nodeName})
	return err
}
