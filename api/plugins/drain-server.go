package plugins

import (
	"context"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	proto "github.com/slyngdk/node-drain/api/plugins/proto/v1"
)

var _ proto.DrainServiceServer = DrainServer{}

type DrainServer struct {
	Logger hclog.Logger
	Impl   DrainPlugin
}

func (s DrainServer) Init(ctx context.Context, request *proto.InitRequest) (*proto.InitResponse, error) {
	info, err := s.Impl.Init(ctx, s.Logger, DrainPluginSettings{})
	if err != nil {
		return nil, err
	}

	return &proto.InitResponse{
		Id: info.ID,
	}, nil
}

func (s DrainServer) IsSupported(ctx context.Context, request *proto.IsSupportedRequest) (*proto.IsSupportedResponse, error) {
	supported, err := s.Impl.IsSupported(ctx)
	return &proto.IsSupportedResponse{Supported: supported}, err
}

func (s DrainServer) IsHealthy(ctx context.Context, request *proto.IsHealthyRequest) (*proto.IsHealthyResponse, error) {
	healthy, err := s.Impl.IsHealthy(ctx)
	return &proto.IsHealthyResponse{Healthy: healthy}, err
}

func (s DrainServer) IsDrainOk(ctx context.Context, request *proto.IsDrainOkRequest) (*proto.IsDrainOkResponse, error) {
	ok, err := s.Impl.IsDrainOk(ctx, request.NodeName)
	return &proto.IsDrainOkResponse{Ok: ok}, err
}

func (s DrainServer) PreDrain(ctx context.Context, request *proto.PreDrainRequest) (*proto.PreDrainResponse, error) {
	err := s.Impl.PreDrain(ctx, request.NodeName)
	return &proto.PreDrainResponse{}, err
}

func (s DrainServer) PostDrain(ctx context.Context, request *proto.PostDrainRequest) (*proto.PostDrainResponse, error) {
	err := s.Impl.PostDrain(ctx, request.NodeName)
	return &proto.PostDrainResponse{}, err
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
