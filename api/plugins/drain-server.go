package plugins

import (
	"context"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	pluginv1 "github.com/slyngdk/node-drain/api/plugins/proto/v1"
	"google.golang.org/protobuf/proto"
)

var _ pluginv1.DrainServiceServer = DrainServer{}

type DrainServer struct {
	Logger hclog.Logger
	Impl   DrainPlugin
}

func (s DrainServer) PluginInfo(ctx context.Context, _ *pluginv1.PluginInfoRequest) (*pluginv1.PluginInfoResponse, error) {
	info, err := s.Impl.PluginInfo(ctx)
	if err != nil {
		return nil, err
	}
	return pluginv1.PluginInfoResponse_builder{
		Id:           proto.String(info.ID),
		ConfigFormat: &info.ConfigFormat,
	}.Build(), nil
}

func (s DrainServer) Init(ctx context.Context, request *pluginv1.InitRequest) (*pluginv1.InitResponse, error) {
	err := s.Impl.Init(ctx, s.Logger, DrainPluginSettings{
		Config: request.GetConfig(),
	})
	if err != nil {
		return nil, err
	}

	return &pluginv1.InitResponse{}, nil
}

func (s DrainServer) IsSupported(ctx context.Context, _ *pluginv1.IsSupportedRequest) (*pluginv1.IsSupportedResponse, error) {
	supported, err := s.Impl.IsSupported(ctx)
	return pluginv1.IsSupportedResponse_builder{
		Supported: proto.Bool(supported),
	}.Build(), err
}

func (s DrainServer) IsHealthy(ctx context.Context, _ *pluginv1.IsHealthyRequest) (*pluginv1.IsHealthyResponse, error) {
	healthy, err := s.Impl.IsHealthy(ctx)
	return pluginv1.IsHealthyResponse_builder{
		Healthy: proto.Bool(healthy),
	}.Build(), err
}

func (s DrainServer) IsDrainOk(ctx context.Context, request *pluginv1.IsDrainOkRequest) (*pluginv1.IsDrainOkResponse, error) {
	ok, err := s.Impl.IsDrainOk(ctx, request.GetNodeName())
	return pluginv1.IsDrainOkResponse_builder{
		Ok: proto.Bool(ok),
	}.Build(), err
}

func (s DrainServer) PreDrain(ctx context.Context, request *pluginv1.PreDrainRequest) (*pluginv1.PreDrainResponse, error) {
	err := s.Impl.PreDrain(ctx, request.GetNodeName())
	return &pluginv1.PreDrainResponse{}, err
}

func (s DrainServer) PostDrain(ctx context.Context, request *pluginv1.PostDrainRequest) (*pluginv1.PostDrainResponse, error) {
	err := s.Impl.PostDrain(ctx, request.GetNodeName())
	return &pluginv1.PostDrainResponse{}, err
}

func Serve(impl DrainPlugin) {
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Trace,
		Output:     os.Stderr,
		JSONFormat: true,
	})

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: Handshake,
		Logger:          logger,
		VersionedPlugins: map[int]plugin.PluginSet{
			0: {
				"drain": &GRPCDrainPlugin{
					Logger: logger,
					Impl:   impl,
				},
			},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
