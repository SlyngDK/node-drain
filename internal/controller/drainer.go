package controller

/* import (
	"context"
	"time"

	"github.com/google/uuid"
	v1 "github.com/slyngdk/node-drain/api/v1"
	"github.com/slyngdk/node-drain/internal/utils"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var _ manager.Runnable = (*Drainer)(nil)
var _ manager.LeaderElectionRunnable = (*Drainer)(nil)

type Drainer struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
	NameSpace  string
}

func (d *Drainer) NeedLeaderElection() bool {
	return true
}

// +kubebuilder:rbac:groups="",namespace=system,resources=pods,verbs=list;watch;create;get;delete;deletecollection

func (d *Drainer) Start(ctx context.Context) error {
	l := zap.S().Named("drainer")

	rebootManager, err := utils.NewRebootManager(l.Desugar(), d.Client, d.RestConfig, d.NameSpace)
	if err != nil {
		l.Fatal("Failed to create reboot manager", zap.Error(err))
	}

	drainTickerInterval, err := getDurationVariation("drainer.drainCheckInterval", "20s")
	if err != nil {
		l.Error("Failed to get 'drainer.drainCheckInterval'", zap.Error(err))
		drainTickerInterval = 20 * time.Second
	}
	drainTicker := time.NewTicker(drainTickerInterval)

	drainRebootCheckInterval, err := getDurationVariation("drainer.rebootCheckInterval", "6h")
	if err != nil {
		l.Error("Failed to get 'drainer.rebootCheckInterval'", zap.Error(err))
		drainRebootCheckInterval = 6 * time.Hour
	}
	rebootCheckTicker := time.NewTicker(drainRebootCheckInterval)

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

			case <-rebootCheckTicker.C:

				nodes := &v1.NodeList{}
				err := d.List(ctx, nodes)
				if err != nil {
					l.Error(err, "Failed to get nodes")
					continue
				}

				for _, n := range nodes.Items {
					_ = n

					before := metav1.NewTime(time.Now().Add(-1 * 60 * time.Second)) // FIXME configure check interval

					if n.Status.RebootRequiredLastChecked == nil ||
						n.Status.RebootRequiredLastChecked.Before(&before) {
						l.Info("Check if reboot is required", "node", n.Name)
						rebootRequired, err := rebootManager.IsRebootRequired(ctx, n.Name)
						if err != nil {
							l.Error(err, "Failed to check if reboot is required")
							continue
						}
						n.Status.RebootRequiredLastChecked = utils.PtrTo(metav1.Now())
						n.Status.RebootRequired = rebootRequired
						if err := d.Status().Update(ctx, &n); err != nil {
							l.Error(err, "Failed to update node status")
							continue
						}
					}
				}

			case <-ctx.Done():
				drainTicker.Stop()
				rebootCheckTicker.Stop()
				return
			}
		}
	}()

	return nil
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

func getDurationVariation(flagKey string, defaultDuration string) (time.Duration, error) {
	variation, _ := ffclient.StringVariation(flagKey, ffcontext.NewEvaluationContext(uuid.NewString()), defaultDuration)
	return time.ParseDuration(variation)
}
*/
