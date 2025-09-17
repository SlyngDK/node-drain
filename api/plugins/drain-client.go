package plugins

import (
	"context"

	"github.com/hashicorp/go-hclog"
	pluginv1 "github.com/slyngdk/node-drain/api/plugins/proto/v1"
	"google.golang.org/protobuf/proto"
)

var _ DrainPlugin = &DrainClient{}

type DrainClient struct{ client pluginv1.DrainServiceClient }

func (c DrainClient) PluginInfo(ctx context.Context) (DrainPluginInfo, error) {
	info, err := c.client.PluginInfo(ctx, &pluginv1.PluginInfoRequest{})
	if err != nil {
		return DrainPluginInfo{}, err
	}
	return DrainPluginInfo{
		ID:           info.GetId(),
		ConfigFormat: info.GetConfigFormat(),
	}, nil
}

func (c DrainClient) Init(ctx context.Context, _ hclog.Logger, settings DrainPluginSettings) error {
	_, err := c.client.Init(ctx, pluginv1.InitRequest_builder{
		Config: settings.Config,
	}.Build())
	if err != nil {
		return err
	}

	return nil
}

func (c DrainClient) IsSupported(ctx context.Context) (bool, error) {
	resp, err := c.client.IsSupported(ctx, &pluginv1.IsSupportedRequest{})
	if err != nil {
		return false, err
	}
	return resp.GetSupported(), nil
}

func (c DrainClient) IsHealthy(ctx context.Context) (bool, error) {
	resp, err := c.client.IsHealthy(ctx, &pluginv1.IsHealthyRequest{})
	if err != nil {
		return false, err
	}
	return resp.GetHealthy(), nil
}

func (c DrainClient) IsDrainOk(ctx context.Context, nodeName string) (bool, error) {
	resp, err := c.client.IsDrainOk(ctx, pluginv1.IsDrainOkRequest_builder{
		NodeName: proto.String(nodeName),
	}.Build())
	if err != nil {
		return false, err
	}
	return resp.GetOk(), nil
}

func (c DrainClient) PreDrain(ctx context.Context, nodeName string) error {
	_, err := c.client.PreDrain(ctx, pluginv1.PreDrainRequest_builder{
		NodeName: proto.String(nodeName),
	}.Build())
	return err
}

func (c DrainClient) PostDrain(ctx context.Context, nodeName string) error {
	_, err := c.client.PostDrain(ctx, pluginv1.PostDrainRequest_builder{
		NodeName: proto.String(nodeName),
	}.Build())
	return err
}
