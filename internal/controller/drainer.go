package controller

import (
	"context"
	v1 "github.com/slyngdk/node-drain/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"
)

type Drainer struct {
	client.Client
	Scheme *runtime.Scheme
}

func (d *Drainer) Start(ctx context.Context) {
	l := log.FromContext(ctx)
	ticker := time.NewTicker(20 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				nodes := &v1.NodeList{}

				err := d.List(context.TODO(), nodes)
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

			case <-ctx.Done():
				ticker.Stop()
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
