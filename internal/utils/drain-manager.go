package utils

import (
	"bytes"
	"context"
	"fmt"
	"time"

	mod "github.com/slyngdk/node-drain/internal/modules"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cenkalti/backoff/v4"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/drain"
)

func NewDrainManager(l *zap.Logger, modules []mod.KubernetesStateful, client client.Client, restConfig *rest.Config, namespace string) (*DrainManager, error) {
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	return &DrainManager{
		l:          l,
		client:     client,
		kubeClient: clientSet,
		config:     restConfig,
		modules:    modules,
		namespace:  namespace,
	}, nil
}

type DrainManager struct {
	l          *zap.Logger
	client     client.Client
	kubeClient *kubernetes.Clientset
	config     *rest.Config
	modules    []mod.KubernetesStateful
	namespace  string
}

func (d *DrainManager) IsHealthy(ctx context.Context) (healthy bool, err error) {
	healthy, err = d.IsClusterHealthy(ctx)
	if !healthy || err != nil {
		return false, err
	}

	allModulesHealthy := true

	for _, m := range d.modules {
		ok, err := m.IsSupported(ctx)
		if err != nil {
			return false, err
		}
		if !ok {
			d.l.Debug("module not supported", zap.String("module", m.Name()))
			continue
		}
		isHealthy, err := m.IsHealthy(ctx)
		if err != nil {
			return false, err
		}
		if !isHealthy {
			d.l.Warn("module is not healthy", zap.String("module", m.Name()))
			allModulesHealthy = false
			continue
		}
	}

	return allModulesHealthy, err
}

func (d *DrainManager) RunPreHooks(ctx context.Context, nodeName string) error {
	for _, m := range d.modules {
		ok, err := m.IsSupported(ctx)
		if err != nil {
			return err
		}
		if !ok {
			d.l.Debug("module not supported", zap.String("module", m.Name()))
			continue
		}
		d.l.Debug("running module PreDrain", zap.String("module", m.Name()))
		err = m.PreDrain(ctx, nodeName)
		if err != nil {
			d.l.Debug("module PreDrain failed", zap.String("module", m.Name()), zap.Error(err))
			return errors.Wrapf(err, "failed PreDrain on module: %s", m.Name())
		}
		d.l.Debug("module PreDrain succeeded without errors", zap.String("module", m.Name()))
	}
	return nil
}

func (d *DrainManager) DrainNode(ctx context.Context, nodeName string, drainGracePeriod time.Duration, drainTimeout time.Duration, skipWaitForDeleteTimeoutSeconds int, dryRun bool) error {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	drainHelper := &drain.Helper{
		Ctx:                             ctx,
		Client:                          d.kubeClient,
		GracePeriodSeconds:              int(drainGracePeriod.Seconds()),
		IgnoreAllDaemonSets:             true,
		Timeout:                         drainTimeout,
		DeleteEmptyDirData:              true,
		SkipWaitForDeleteTimeoutSeconds: skipWaitForDeleteTimeoutSeconds,
		Out:                             stdout,
		ErrOut:                          stderr,
	}

	if dryRun {
		drainHelper.DryRunStrategy = cmdutil.DryRunServer
	}

	d.l.Info("draining node",
		zap.String("node.name", nodeName),
		zap.Bool("dryRun", dryRun))

	err := drain.RunNodeDrain(drainHelper, nodeName)
	if err != nil {
		d.l.Error("failed to drain node",
			zap.String("node.name", nodeName),
			zap.String("stdout", stdout.String()),
			zap.String("stderr", stderr.String()),
			zap.Bool("dryRun", dryRun))
		return errors.Wrapf(err, "failed to drain node: %s", nodeName)
	}

	d.l.Info("drained node",
		zap.String("node.name", nodeName),
		zap.String("stdout", stdout.String()),
		zap.String("stderr", stderr.String()),
		zap.Bool("dryRun", dryRun))

	return nil
}

func (d *DrainManager) RunPostHooks(ctx context.Context, nodeName string, dryRun bool) error {
	var b backoff.BackOff
	b = backoff.NewExponentialBackOff()
	b = backoff.WithContext(b, ctx)

	isClusterHealty := func() error {
		healthy, err := d.IsClusterHealthy(ctx)
		if err != nil {
			d.l.Error("error checking if cluster is healthy", zap.Error(err))
			return err
		}
		if !healthy {
			return fmt.Errorf("cluster is not healthy yet")
		}
		return nil
	}
	err := backoff.Retry(isClusterHealty, b)
	if err != nil {
		return err
	}
	b.Reset()

	err = d.UncordonNode(ctx, nodeName, dryRun)
	if err != nil {
		return err
	}

	for _, m := range d.modules {
		ok, err := m.IsSupported(ctx)
		if err != nil {
			return err
		}
		if !ok {
			d.l.Debug("module not supported", zap.String("module", m.Name()))
			continue
		}
		d.l.Debug("running module PostDrain", zap.String("module", m.Name()))
		err = m.PostDrain(ctx, nodeName)
		if err != nil {
			d.l.Debug("module PostDrain failed", zap.String("module", m.Name()), zap.Error(err))
			return errors.Wrapf(err, "failed PostDrain on module: %s", m.Name())
		}
		d.l.Debug("module PostDrain succeeded without errors", zap.String("module", m.Name()))
	}
	return nil
}

func (d *DrainManager) IsClusterHealthy(ctx context.Context) (bool, error) {
	nodes, _ := d.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})

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

func (d *DrainManager) IsDrainOk(ctx context.Context, nodeName string) (bool, error) {
	allModulesReadyToDrain := true

	for _, m := range d.modules {
		ok, err := m.IsSupported(ctx)
		if err != nil {
			return false, err
		}
		if !ok {
			d.l.Debug("module not supported", zap.String("module", m.Name()))
			continue
		}
		isDrainOk, err := m.IsDrainOk(ctx, nodeName)
		if err != nil {
			return false, err
		}
		if !isDrainOk {
			d.l.Warn("module is not ready to be drained", zap.String("module", m.Name()))
			allModulesReadyToDrain = false
			continue
		}
	}

	return allModulesReadyToDrain, nil
}

func (d *DrainManager) NodeExists(ctx context.Context, nodeName string) (bool, bool, error) {
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, err
	}

	return true, node.Spec.Unschedulable, err
}

func (d *DrainManager) CordonNode(ctx context.Context, nodeName string, dryRun bool) error {
	d.l.Debug("Cordon node", zap.String("nodeName", nodeName))
	return d.cordonNode(ctx, nodeName, true, dryRun)
}

func (d *DrainManager) UncordonNode(ctx context.Context, nodeName string, dryRun bool) error {
	d.l.Debug("Uncordon node", zap.String("nodeName", nodeName))
	return d.cordonNode(ctx, nodeName, false, dryRun)
}

func (d *DrainManager) cordonNode(ctx context.Context, nodeName string, cordon, dryRun bool) error {
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	cordonHelper := drain.NewCordonHelper(node)

	if !cordonHelper.UpdateIfRequired(cordon) {
		return nil
	}

	err, _ = cordonHelper.PatchOrReplaceWithContext(ctx, d.kubeClient, dryRun)
	if err != nil {
		return errors.Wrapf(err, "failed to un/cordon node: %s", nodeName)
	}
	return nil
}
