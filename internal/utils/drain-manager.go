package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
				Stderr:           PluginOutputMonitor("Stderr", pluginLogger),
				SyncStdout:       PluginOutputMonitor("SyncStdout", pluginLogger),
				SyncStderr:       PluginOutputMonitor("SyncStdout", pluginLogger),
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

// func NewDrainManager(l *zap.Logger, modules []mod.KubernetesStateful, client client.Client, restConfig *rest.Config, namespace string) (*DrainManager, error) {
// 	clientSet, err := kubernetes.NewForConfig(restConfig)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &DrainManager{
// 		l:          l,
// 		client:     client,
// 		kubeClient: clientSet,
// 		config:     restConfig,
// 		modules:    modules,
// 		namespace:  namespace,
// 	}, nil
// }
//
// type DrainManager struct {
// 	l          *zap.Logger
// 	client     client.Client
// 	kubeClient *kubernetes.Clientset
// 	config     *rest.Config
// 	modules    []mod.KubernetesStateful
// 	namespace  string
// }
//
// func (d *DrainManager) IsHealthy(ctx context.Context) (healthy bool, err error) {
// 	healthy, err = d.IsClusterNodesHealthy(ctx)
// 	if !healthy || err != nil {
// 		return false, err
// 	}
//
// 	allModulesHealthy := true
//
// 	for _, m := range d.modules {
// 		ok, err := m.IsSupported(ctx)
// 		if err != nil {
// 			return false, err
// 		}
// 		if !ok {
// 			d.l.Debug("module not supported", zap.String("module", m.Name()))
// 			continue
// 		}
// 		isHealthy, err := m.IsHealthy(ctx)
// 		if err != nil {
// 			return false, err
// 		}
// 		if !isHealthy {
// 			d.l.Warn("module is not healthy", zap.String("module", m.Name()))
// 			allModulesHealthy = false
// 			continue
// 		}
// 	}
//
// 	return allModulesHealthy, err
// }
//
// func (d *DrainManager) RunPreHooks(ctx context.Context, nodeName string) error {
// 	for _, m := range d.modules {
// 		ok, err := m.IsSupported(ctx)
// 		if err != nil {
// 			return err
// 		}
// 		if !ok {
// 			d.l.Debug("module not supported", zap.String("module", m.Name()))
// 			continue
// 		}
// 		d.l.Debug("running module PreDrain", zap.String("module", m.Name()))
// 		err = m.PreDrain(ctx, nodeName)
// 		if err != nil {
// 			d.l.Debug("module PreDrain failed", zap.String("module", m.Name()), zap.Error(err))
// 			return errors.Wrapf(err, "failed PreDrain on module: %s", m.Name())
// 		}
// 		d.l.Debug("module PreDrain succeeded without errors", zap.String("module", m.Name()))
// 	}
// 	return nil
// }
//
// func (d *DrainManager) DrainNode(ctx context.Context, nodeName string, drainGracePeriod time.Duration, drainTimeout time.Duration, skipWaitForDeleteTimeoutSeconds int, dryRun bool) error {
// 	stdout := new(bytes.Buffer)
// 	stderr := new(bytes.Buffer)
//
// 	drainHelper := &drain.Helper{
// 		Ctx:                             ctx,
// 		Client:                          d.kubeClient,
// 		GracePeriodSeconds:              int(drainGracePeriod.Seconds()),
// 		IgnoreAllDaemonSets:             true,
// 		Timeout:                         drainTimeout,
// 		DeleteEmptyDirData:              true,
// 		SkipWaitForDeleteTimeoutSeconds: skipWaitForDeleteTimeoutSeconds,
// 		Out:                             stdout,
// 		ErrOut:                          stderr,
// 	}
//
// 	if dryRun {
// 		drainHelper.DryRunStrategy = cmdutil.DryRunServer
// 	}
//
// 	d.l.Info("draining node",
// 		zap.String("node.name", nodeName),
// 		zap.Bool("dryRun", dryRun))
//
// 	err := drain.RunNodeDrain(drainHelper, nodeName)
// 	if err != nil {
// 		d.l.Error("failed to drain node",
// 			zap.String("node.name", nodeName),
// 			zap.String("stdout", stdout.String()),
// 			zap.String("stderr", stderr.String()),
// 			zap.Bool("dryRun", dryRun))
// 		return errors.Wrapf(err, "failed to drain node: %s", nodeName)
// 	}
//
// 	d.l.Info("drained node",
// 		zap.String("node.name", nodeName),
// 		zap.String("stdout", stdout.String()),
// 		zap.String("stderr", stderr.String()),
// 		zap.Bool("dryRun", dryRun))
//
// 	return nil
// }
//
// func (d *DrainManager) RunPostHooks(ctx context.Context, nodeName string, dryRun bool) error {
// 	var b backoff.BackOff
// 	b = backoff.NewExponentialBackOff()
// 	b = backoff.WithContext(b, ctx)
//
// 	isClusterHealty := func() error {
// 		healthy, err := d.IsClusterNodesHealthy(ctx)
// 		if err != nil {
// 			d.l.Error("error checking if cluster is healthy", zap.Error(err))
// 			return err
// 		}
// 		if !healthy {
// 			return fmt.Errorf("cluster is not healthy yet")
// 		}
// 		return nil
// 	}
// 	err := backoff.Retry(isClusterHealty, b)
// 	if err != nil {
// 		return err
// 	}
// 	b.Reset()
//
// 	err = d.UncordonNode(ctx, nodeName, dryRun)
// 	if err != nil {
// 		return err
// 	}
//
// 	for _, m := range d.modules {
// 		ok, err := m.IsSupported(ctx)
// 		if err != nil {
// 			return err
// 		}
// 		if !ok {
// 			d.l.Debug("module not supported", zap.String("module", m.Name()))
// 			continue
// 		}
// 		d.l.Debug("running module PostDrain", zap.String("module", m.Name()))
// 		err = m.PostDrain(ctx, nodeName)
// 		if err != nil {
// 			d.l.Debug("module PostDrain failed", zap.String("module", m.Name()), zap.Error(err))
// 			return errors.Wrapf(err, "failed PostDrain on module: %s", m.Name())
// 		}
// 		d.l.Debug("module PostDrain succeeded without errors", zap.String("module", m.Name()))
// 	}
// 	return nil
// }
//
// func (d *DrainManager) IsClusterNodesHealthy(ctx context.Context) (bool, error) {
// 	nodes, _ := d.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
//
// 	allNodesAreReady := true
//
// 	for _, node := range nodes.Items {
// 		for _, condition := range node.Status.Conditions {
// 			if condition.Type == corev1.NodeReady {
// 				if condition.Status != corev1.ConditionTrue {
// 					d.l.Warn("node is not ready", zap.String("node.name", node.Name), zap.String("node.ready", string(condition.Status)))
// 					allNodesAreReady = false
// 				}
// 			}
// 		}
// 	}
// 	return allNodesAreReady, nil
// }
//
// func (d *DrainManager) IsDrainOk(ctx context.Context, nodeName string) (bool, error) {
// 	allModulesReadyToDrain := true
//
// 	for _, m := range d.modules {
// 		ok, err := m.IsSupported(ctx)
// 		if err != nil {
// 			return false, err
// 		}
// 		if !ok {
// 			d.l.Debug("module not supported", zap.String("module", m.Name()))
// 			continue
// 		}
// 		isDrainOk, err := m.IsDrainOk(ctx, nodeName)
// 		if err != nil {
// 			return false, err
// 		}
// 		if !isDrainOk {
// 			d.l.Warn("module is not ready to be drained", zap.String("module", m.Name()))
// 			allModulesReadyToDrain = false
// 			continue
// 		}
// 	}
//
// 	return allModulesReadyToDrain, nil
// }
//
// func (d *DrainManager) NodeExists(ctx context.Context, nodeName string) (bool, bool, error) {
// 	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
// 	if err != nil {
// 		if kerrors.IsNotFound(err) {
// 			return false, false, nil
// 		}
// 		return false, false, err
// 	}
//
// 	return true, node.Spec.Unschedulable, err
// }
//
// func (d *DrainManager) CordonNode(ctx context.Context, nodeName string, dryRun bool) error {
// 	d.l.Debug("Cordon node", zap.String("nodeName", nodeName))
// 	return d.cordonNode(ctx, nodeName, true, dryRun)
// }
//
// func (d *DrainManager) UncordonNode(ctx context.Context, nodeName string, dryRun bool) error {
// 	d.l.Debug("Uncordon node", zap.String("nodeName", nodeName))
// 	return d.cordonNode(ctx, nodeName, false, dryRun)
// }
//
// func (d *DrainManager) cordonNode(ctx context.Context, nodeName string, cordon, dryRun bool) error {
// 	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
// 	if err != nil {
// 		return err
// 	}
//
// 	cordonHelper := drain.NewCordonHelper(node)
//
// 	if !cordonHelper.UpdateIfRequired(cordon) {
// 		return nil
// 	}
//
// 	err, _ = cordonHelper.PatchOrReplaceWithContext(ctx, d.kubeClient, dryRun)
// 	if err != nil {
// 		return errors.Wrapf(err, "failed to un/cordon node: %s", nodeName)
// 	}
// 	return nil
// }
