package utils

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-plugin"
	"github.com/slyngdk/node-drain/api/plugins"
	pluginv1 "github.com/slyngdk/node-drain/api/plugins/proto/v1"
	drainv1 "github.com/slyngdk/node-drain/api/v1"
	"github.com/slyngdk/node-drain/internal/config"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	kClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const pluginNotSupported = "Plugin not supported"

type drainClientInfo struct {
	*plugins.DrainClient
	info *plugins.DrainPluginInfo
}

type DrainManager struct {
	l             *zap.Logger
	client        kClient.Client
	clientSet     *kubernetes.Clientset
	recorder      record.EventRecorder
	pluginClients map[string]*plugin.Client
	drainClients  map[string]drainClientInfo
	pluginFileId  map[string]string
}

func NewDrainManager(ctx context.Context, client kClient.Client, restConfig *rest.Config, recorder record.EventRecorder) (*DrainManager, error) {
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
		recorder:     recorder,
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

	l.Debug("Getting plugin info")
	pluginInfo, err := drainClient.PluginInfo(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get plugin info for plugin %s: %w", pluginBase, err)
	}
	if pluginInfo.ID == "" {
		return nil, nil, fmt.Errorf("drain plugin %s has no ID", pluginBase)
	}
	if pluginInfo.ConfigFormat == pluginv1.ConfigFormat_CONFIG_FORMAT_UNSPECIFIED {
		return nil, nil, fmt.Errorf("drain plugin %s has no ConfigFormat", pluginBase)
	}

	pluginConfigBytes, err := config.GetPluginConfig(pluginInfo.ConfigFormat, pluginInfo.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get plugin config bytes %s: %w", pluginBase, err)
	}

	l.Debug("Calling drain plugin Init()")
	err = drainClient.Init(ctx, Wrap(l),
		plugins.DrainPluginSettings{
			Config: pluginConfigBytes,
		})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to init plugin %s: %w", pluginBase, err)
	}

	return &pluginInfo, drainClient, nil
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
					d.l.Warn("node is not ready", zap.String(config.LogNodeName, node.Name), zap.String("node.ready", string(condition.Status)))
					allNodesAreReady = false
				}
			}
		}
	}
	return allNodesAreReady, nil
}

func (d *DrainManager) IsHealthy(ctx context.Context, node *drainv1.Node) (bool, error) {
	d.l.Debug("Checking if cluster is healthy, including drain plugins.")
	healthy, err := d.IsClusterNodesHealthy(ctx)
	if !healthy || err != nil {
		d.recorder.Eventf(node, corev1.EventTypeWarning, "Healthy", "Cluster nodes is not healthy: %t err: %v", healthy, err)
		return false, err
	}

	allModulesHealthy := true
	errs := make([]error, 0)
	pluginStatus := make([]string, 0, len(d.drainClients))

	for id, client := range d.drainClients {
		l := d.l.With(zap.String(config.LogPluginId, id))
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			allModulesHealthy = false
			errs = append(errs, err)
			pluginStatus = append(pluginStatus, pluginEventError(id, err))
			continue
		}
		if !isSupported {
			l.Debug(pluginNotSupported)
			pluginStatus = append(pluginStatus, fmt.Sprintf("%s: %s", id, pluginNotSupported))
			continue
		}

		l.Debug("Checking if drain plugin is healthy")
		isHealthy, err := client.IsHealthy(ctx)
		if err != nil {
			allModulesHealthy = false
			errs = append(errs, err)
			pluginStatus = append(pluginStatus, pluginEventError(id, err))
			continue
		}
		if !isHealthy {
			l.Warn("Plugin is not healthy")
			allModulesHealthy = false
			pluginStatus = append(pluginStatus, fmt.Sprintf("%s: Not healthy", id))
			continue
		}

		pluginStatus = append(pluginStatus, fmt.Sprintf("%s: OK", id))
	}
	eventType := corev1.EventTypeNormal
	if !allModulesHealthy || len(errs) > 0 {
		eventType = corev1.EventTypeWarning
	}
	d.recorder.Eventf(node, eventType, "Healthy", "IsHealthy:\n%s", strings.Join(pluginStatus, "\n"))

	if len(errs) != 0 {
		return false, errors.Join(errs...)
	}
	return allModulesHealthy, err
}

func (d *DrainManager) IsDrainOk(ctx context.Context, node *drainv1.Node) (bool, error) {
	l := d.l.With(zap.String(config.LogNodeName, node.Name))
	l.Debug("Checking if drain is OK")
	allModulesReadyToDrain := true
	errs := make([]error, 0)
	pluginStatus := make([]string, 0, len(d.drainClients))

	for id, client := range d.drainClients {
		lp := l.With(zap.String(config.LogPluginId, id))
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			allModulesReadyToDrain = false
			errs = append(errs, err)
			pluginStatus = append(pluginStatus, pluginEventError(id, err))
			continue
		}
		if !isSupported {
			lp.Debug(pluginNotSupported)
			pluginStatus = append(pluginStatus, fmt.Sprintf("%s: %s", id, pluginNotSupported))
			continue
		}

		isDrainOk, err := client.IsDrainOk(ctx, node.Name)
		if err != nil {
			allModulesReadyToDrain = false
			errs = append(errs, err)
			pluginStatus = append(pluginStatus, pluginEventError(id, err))
			continue
		}
		if !isDrainOk {
			lp.Warn("plugin is not ready to be drained")
			allModulesReadyToDrain = false
			pluginStatus = append(pluginStatus, fmt.Sprintf("%s: Not ready to be drained", id))
			continue
		}

		pluginStatus = append(pluginStatus, fmt.Sprintf("%s: OK", id))
	}

	eventType := corev1.EventTypeNormal
	if !allModulesReadyToDrain || len(errs) > 0 {
		eventType = corev1.EventTypeWarning
	}
	d.recorder.Eventf(node, eventType, "DrainOK", "IsDrainOK:\n%s", strings.Join(pluginStatus, "\n"))

	if len(errs) != 0 {
		return false, errors.Join(errs...)
	}

	return allModulesReadyToDrain, nil
}

func (d *DrainManager) RunPreDrain(ctx context.Context, node *drainv1.Node) error {
	l := d.l.With(zap.String(config.LogNodeName, node.Name))
	l.Debug("Run PreDrain")

	for id, client := range d.drainClients {
		lp := l.With(zap.String(config.LogPluginId, id))
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			return err
		}
		if !isSupported {
			lp.Debug(pluginNotSupported)
			continue
		}

		lp.Debug("Running plugin PreDrain")
		err = client.PreDrain(ctx, node.Name)
		if err != nil {
			lp.Debug("Plugin PreDrain failed", zap.Error(err))
			d.recorder.Eventf(node, corev1.EventTypeWarning, "PreDrain", "PreDrain failed for %s: %v", id, err)
			return fmt.Errorf("failed PreDrain on plugin: %s %w", id, err)
		}
		lp.Debug("Plugin PreDrain succeeded without errors")
	}
	d.recorder.Eventf(node, corev1.EventTypeNormal, "PreDrain", "Node PreDrain succeeded")
	return nil
}

func (d *DrainManager) RunPostDrain(ctx context.Context, node *drainv1.Node) error {
	l := d.l.With(zap.String(config.LogNodeName, node.Name))
	l.Debug("Run PostDrain")

	for id, client := range d.drainClients {
		lp := l.With(zap.String(config.LogPluginId, id))
		isSupported, err := client.IsSupported(ctx)
		if err != nil {
			return err
		}
		if !isSupported {
			lp.Debug(pluginNotSupported)
			continue
		}

		lp.Debug("Running plugin PostDrain")
		err = client.PostDrain(ctx, node.Name)
		if err != nil {
			lp.Debug("Plugin PostDrain failed", zap.Error(err))
			d.recorder.Eventf(node, corev1.EventTypeWarning, "PostDrain", "PostDrain failed for %s: %v", id, err)
			return fmt.Errorf("failed PostDrain on plugin: %s %w", id, err)
		}
		lp.Debug("Plugin PostDrain succeeded without errors")
	}
	d.recorder.Eventf(node, corev1.EventTypeNormal, "PostDrain", "Node PostDrain succeeded")
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
					l = l.With(zap.String(config.LogPluginId, id))
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

func pluginEventError(id string, err error) string {
	return fmt.Sprintf("%s: Error: %s", id, err.Error())
}
