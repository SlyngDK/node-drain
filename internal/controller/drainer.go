package controller

import (
	"context"
	v1 "github.com/slyngdk/node-drain/api/v1"
	"github.com/slyngdk/node-drain/internal/utils"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type Drainer struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
	NameSpace  string
}

//+kubebuilder:rbac:groups="",namespace=$(SERVICE_NAMESPACE),resources=pods,verbs=list;watch;create;get;delete;deletecollection

func (d *Drainer) Start(ctx context.Context) {
	l := zap.S().Named("drainer")

	rebootManager, err := utils.NewRebootManager(l.Desugar(), d.Client, d.RestConfig, d.NameSpace)
	if err != nil {
		l.Fatal("Failed to create reboot manager", zap.Error(err))
	}

	drainTicker := time.NewTicker(20 * time.Second) //FIXME
	checkTicker := time.NewTicker(20 * time.Second) //FIXME
	go func() {
		for {
			select {
			case <-drainTicker.C:
				nodes := &v1.NodeList{}

				err := d.List(ctx, nodes)
				if err != nil {
					l.Error(err, "Failed to get nodes")
					continue
				}

				if len(nodes.Items) == 0 {
					continue
				}

				node := getActiveNode(nodes)
				if node == nil {
					node = getNextNode(nodes)
					if node == nil {
						continue
					}
					node.Status.Status = v1.NodeDrainStatusNext
					if err := d.Status().Update(ctx, node); err != nil {
						l.Error(err, "Failed to update node status")
						continue
					}
					continue
				}

			case <-checkTicker.C:

				nodes := &v1.NodeList{}
				err := d.List(ctx, nodes)
				if err != nil {
					l.Error(err, "Failed to get nodes")
					continue
				}

				for _, n := range nodes.Items {
					_ = n

					before := metav1.NewTime(time.Now().Add(-1 * 60 * time.Second)) //FIXME configure check interval

					if n.Status.RebootRequiredLastChecked == nil ||
						n.Status.RebootRequiredLastChecked.Before(&before) {
						l.Info("Check if reboot is required", "node", n.Name)
						rebootRequired, err := rebootManager.IsRebootRequired(ctx, n.Name)
						if err != nil {
							l.Error(err, "Failed to check if reboot is required")
							continue
						}
						now := metav1.Now()
						n.Status.RebootRequiredLastChecked = &now
						n.Status.RebootRequired = rebootRequired
						if err := d.Status().Update(ctx, &n); err != nil {
							l.Error(err, "Failed to update node status")
							continue
						}
					}
				}

			case <-ctx.Done():
				drainTicker.Stop()
				return
			}
		}
	}()
}

func getActiveNode(nodes *v1.NodeList) *v1.Node {
	for _, n := range nodes.Items {
		switch n.Status.Status {
		case v1.NodeDrainStatusNext:
			return &n
		}
	}
	return nil
}

func getNextNode(nodes *v1.NodeList) *v1.Node {
	for _, n := range nodes.Items {
		switch n.Status.Status {
		case v1.NodeDrainStatusQueued:
			return &n
		}
	}
	return nil
}
