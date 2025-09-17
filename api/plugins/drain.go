package plugins

import (
	"context"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pluginv1 "github.com/slyngdk/node-drain/api/plugins/proto/v1"
)

var Handshake = plugin.HandshakeConfig{
	MagicCookieKey:   "NODEDRAIN_PLUGIN",
	MagicCookieValue: "4ae46e30-c5de-4ab9-a2c5-4618fdcab7ae",
}

type DrainPluginSettings struct {
	Config []byte
}

type DrainPluginInfo struct {
	ID           string
	ConfigFormat pluginv1.ConfigFormat
}

type DrainPlugin interface {
	PluginInfo(ctx context.Context) (DrainPluginInfo, error)
	Init(ctx context.Context, logger hclog.Logger, settings DrainPluginSettings) error
	IsSupported(ctx context.Context) (bool, error)
	IsHealthy(ctx context.Context) (bool, error)
	IsDrainOk(ctx context.Context, nodeName string) (bool, error)
	PreDrain(ctx context.Context, nodeName string) error
	PostDrain(ctx context.Context, nodeName string) error
}

var _ plugin.GRPCPlugin = GRPCDrainPlugin{}

type GRPCDrainPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Logger hclog.Logger
	Impl   DrainPlugin
}

func (p GRPCDrainPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pluginv1.RegisterDrainServiceServer(s, &DrainServer{
		Logger: p.Logger,
		Impl:   p.Impl,
	})
	return nil
}

func (p GRPCDrainPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &DrainClient{client: pluginv1.NewDrainServiceClient(c)}, nil
}
