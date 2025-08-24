package utils

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-plugin"
	"github.com/pkg/errors"
	"github.com/slyngdk/node-drain/api/plugins"
	"github.com/slyngdk/node-drain/internal/config"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	kClient "sigs.k8s.io/controller-runtime/pkg/client"
)

type drainClientInfo struct {
	*plugins.DrainClient
	info *plugins.DrainPluginInfo
}

type DrainManager struct {
	l             *zap.Logger
	client        kClient.Client
	clientSet     *kubernetes.Clientset
	pluginClients map[string]*plugin.Client
	drainClients  map[string]drainClientInfo
	pluginFileId  map[string]string
}

func NewDrainManager(ctx context.Context, client kClient.Client, restConfig *rest.Config) (*DrainManager, error) {
	l, err := config.GetNamedLogger("drain-plugin-client")
	if err != nil {
		return nil, err
	}

	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	d := &DrainManager{
		l:            l,
		client:       client,
		clientSet:    clientSet,
		drainClients: make(map[string]drainClientInfo),
		pluginFileId: make(map[string]string),
	}

	err = d.loadPluginsFromDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load plugins: %w", err)
	}

	return d, nil
}

func (d *DrainManager) loadPluginsFromDir(ctx context.Context) error {
	path := "/plugins"

	d.l.Debug("Loading plugins from path", zap.String("plugin.path", path))

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("failed to load plugins: %w", err)
	}

	foundClients := make(map[string]*plugin.Client)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".so" {
			pluginPath := filepath.Join(path, e.Name())
			pluginBase := filepath.Base(pluginPath)

			pluginLogger := d.l.With(zap.String("plugin.file", pluginBase))
			pluginLogger.Debug("Creating new client for plugin")
			client := plugin.NewClient(&plugin.ClientConfig{
				HandshakeConfig: plugins.Handshake,
				VersionedPlugins: map[int]plugin.PluginSet{
					0: {
						"drain": plugins.GRPCDrainPlugin{},
					},
				},
				Cmd:              exec.Command(pluginPath),
				Managed:          true,
				Stderr:           d.pluginOutputMonitor("Stderr", pluginLogger, pluginBase),
				SyncStdout:       d.pluginOutputMonitor("SyncStdout", pluginLogger, pluginBase),
				SyncStderr:       d.pluginOutputMonitor("SyncStdout", pluginLogger, pluginBase),
				AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
				Logger:           Wrap(pluginLogger),
			})
			foundClients[pluginPath] = client

			info, drainClient, err := d.initPlugin(ctx, pluginLogger, pluginBase, client)
			if err != nil {
				return fmt.Errorf("failed to initialize plugin %s: %w", pluginBase, err)
			}

			if _, ok := d.drainClients[info.ID]; ok {
				return fmt.Errorf("drain plugin %s(%s) already initialized", pluginBase, info.ID)
			}
			d.drainClients[info.ID] = drainClientInfo{drainClient, info}
			d.pluginFileId[pluginBase] = info.ID
		}
	}
	d.pluginClients = foundClients
	return nil
}

func (d *DrainManager) initPlugin(ctx context.Context, l *zap.Logger, pluginBase string, client *plugin.Client) (*plugins.DrainPluginInfo, *plugins.DrainClient, error) {
	l.Debug("Initializing plugin")
	c, err := client.Client()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get client for plugin %s: %w", pluginBase, err)
	}

	l.Debug("Pinging plugin")
	if err = c.Ping(); err != nil {
		return nil, nil, fmt.Errorf("failed to ping plugin %s: %w", pluginBase, err)
	}

	l.Debug("Getting instance of drain plugin")
	v, err := c.Dispense("drain")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to dispense plugin %s: %w", pluginBase, err)
	}

	drainClient, ok := v.(*plugins.DrainClient)
	if !ok {
		return nil, nil, fmt.Errorf("expected drain plugin %s: %T", pluginBase, v)
	}

	l.Debug("Calling drain plugin Init()")
	info, err := drainClient.Init(ctx, Wrap(l), plugins.DrainPluginSettings{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to init plugin %s: %w", pluginBase, err)
	}

	if info.ID == "" {
		return nil, nil, fmt.Errorf("drain plugin %s has no ID", pluginBase)
	}
	return &info, drainClient, nil
}

func (d *DrainManager) IsClusterNodesHealthy(ctx context.Context) (bool, error) {
	d.l.Debug("Checking if cluster nodes are healthy")
	nodes, err := d.clientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	allNodesAreReady := true

	for _, node := range nodes.Items {
		// TODO Check all conditions and these is up to date (Cilium is not updating)
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				if condition.Status != corev1.ConditionTrue {
					d.l.Warn("node is not ready", zap.String("node.name", node.Name), zap.String("node.ready", string(condition.Status)))
					allNodesAreReady = false
				}
			}
		}
	}
	return allNodesAreReady, nil
}

func (d *DrainManager) IsHealthy(ctx context.Context) (bool, error) {
	d.l.Debug("Checking if cluster is healthy, including drain plugins.")
	healthy, err := d.IsClusterNodesHealthy(ctx)
	if !healthy || err != nil {
		return false, err
	}

	allModulesHealthy := true

	for id, client := range d.drainClients {
		l := d.l.With(zap.String("plugin.id", id))
		l.Debug("Checking if drain plugin is supported")
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			return false, err
		}
		if !isSupported {
			d.l.Debug("Plugin not supported")
			continue
		}

		l.Debug("Checking if drain plugin is healthy")
		isHealthy, err := client.IsHealthy(ctx)
		if err != nil {
			return false, err
		}
		if !isHealthy {
			d.l.Warn("Plugin is not healthy")
			allModulesHealthy = false
			continue
		}
	}

	return allModulesHealthy, err
}

func (d *DrainManager) IsDrainOk(ctx context.Context, nodeName string) (bool, error) {
	l := d.l.With(zap.String("node.name", nodeName))
	l.Debug("Checking if drain is OK")
	allModulesReadyToDrain := true

	for id, client := range d.drainClients {
		lp := l.With(zap.String("plugin.id", id))
		lp.Debug("Checking if drain plugin is supported")
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			return false, err
		}
		if !isSupported {
			d.l.Debug("Plugin not supported")
			continue
		}

		isDrainOk, err := client.IsDrainOk(ctx, nodeName)
		if err != nil {
			return false, err
		}
		if !isDrainOk {
			lp.Warn("plugin is not ready to be drained")
			allModulesReadyToDrain = false
			continue
		}
	}

	return allModulesReadyToDrain, nil
}

func (d *DrainManager) RunPreDrain(ctx context.Context, nodeName string) error {
	l := d.l.With(zap.String("node.name", nodeName))
	l.Debug("Run PreDrain")

	for id, client := range d.drainClients {
		lp := l.With(zap.String("plugin.id", id))
		lp.Debug("Checking if drain plugin is supported")
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			return err
		}
		if !isSupported {
			d.l.Debug("Plugin not supported")
			continue
		}

		lp.Debug("Running plugin PreDrain")
		err = client.PreDrain(ctx, nodeName)
		if err != nil {
			lp.Debug("Plugin PreDrain failed", zap.Error(err))
			return errors.Wrapf(err, "failed PreDrain on plugin: %s", id)
		}
		lp.Debug("Plugin PreDrain succeeded without errors")
	}
	return nil
}

func (d *DrainManager) RunPostDrain(ctx context.Context, nodeName string) error {
	l := d.l.With(zap.String("node.name", nodeName))
	l.Debug("Run PostDrain")

	for id, client := range d.drainClients {
		lp := l.With(zap.String("plugin.id", id))
		lp.Debug("Checking if drain plugin is supported")
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			return err
		}
		if !isSupported {
			d.l.Debug("Plugin not supported")
			continue
		}

		lp.Debug("Running plugin PostDrain")
		err = client.PostDrain(ctx, nodeName)
		if err != nil {
			lp.Debug("Plugin PostDrain failed", zap.Error(err))
			return errors.Wrapf(err, "failed PostDrain on plugin: %s", id)
		}
		lp.Debug("Plugin PostDrain succeeded without errors")
	}
	return nil
}

func (d *DrainManager) pluginOutputMonitor(streamName string, l *zap.Logger, pluginFile string) io.Writer {
	l = l.WithOptions(zap.WithCaller(false), zap.AddStacktrace(zap.FatalLevel)).Named(streamName)
	reader, writer := io.Pipe()

	go func() {
		addedPluginId := false
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 1024), 1024*1024) // 1MB max buffer
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if len(line) == 0 {
				continue
			}

			if !addedPluginId {
				if id, ok := d.pluginFileId[pluginFile]; ok {
					l = l.With(zap.String("plugin.id", id))
					addedPluginId = true
				}
			}

			err := logJsonLog(l, line)
			if err != nil {
				switch line := line; {
				case strings.HasPrefix(line, "[TRACE]"):
					l.Log(config.TraceLevel, line)
				case strings.HasPrefix(line, "[DEBUG]"):
					l.Debug(line)
				case strings.HasPrefix(line, "[INFO]"):
					l.Info(line)
				case strings.HasPrefix(line, "[WARN]"):
					l.Warn(line)
				case strings.HasPrefix(line, "[ERROR]"):
					l.Error(line)
				case strings.HasPrefix(line, "panic: ") || strings.HasPrefix(line, "fatal error: "):
					l.Error(line)
				default:
					l.Info(line)
				}
			}
		}
	}()

	return writer
}
