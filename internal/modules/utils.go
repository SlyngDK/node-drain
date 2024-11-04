package modules

import (
	"context"
	"fmt"
	"io"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"regexp"
	"strings"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
)

// ExecCmd exec command on specific pod and wait the command's output.
func ExecCmd(ctx context.Context, client kubernetes.Interface, config *rest.Config, pod types.NamespacedName,
	command string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	cmd := []string{
		"sh",
		"-c",
		command,
	}
	req := client.CoreV1().RESTClient().Post().Resource("pods").Name(pod.Name).
		Namespace(pod.Namespace).SubResource("exec")
	option := &corev1.PodExecOptions{
		Command: cmd,
		Stdin:   true,
		Stdout:  true,
		Stderr:  true,
		TTY:     true,
	}
	if stdin == nil {
		option.Stdin = false
	}
	req.VersionedParams(
		option,
		scheme.ParameterCodec,
	)
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return err
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		return err
	}

	return nil
}

func IsPodCompleted(c kubernetes.Interface, podName, namespace string) wait.ConditionWithContextFunc {
	return func(ctx context.Context) (bool, error) {
		pod, err := c.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		switch pod.Status.Phase {
		case corev1.PodFailed, corev1.PodSucceeded:
			return true, nil
		}
		return false, nil
	}
}

func IsPodRunning(c kubernetes.Interface, podName, namespace string) wait.ConditionWithContextFunc {
	return func(ctx context.Context) (bool, error) {
		pod, err := c.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		switch pod.Status.Phase {
		case corev1.PodFailed, corev1.PodSucceeded:
			return false, fmt.Errorf("pod is in unexpected phase: %s", pod.Status.Phase)
		case corev1.PodRunning:
			return true, nil
		}
		return false, nil
	}
}

func IsNodeRebooted(l *zap.Logger, c kubernetes.Interface, nodeName, bootIdOld string) wait.ConditionWithContextFunc {
	return func(ctx context.Context) (bool, error) {
		node, err := c.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			l.Warn("error getting node", zap.Error(err))
			return false, nil
		}

		if node.Status.NodeInfo.BootID != bootIdOld {
			nodeReady := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					if condition.Status == corev1.ConditionTrue {
						nodeReady = true
					}
				}
			}
			return nodeReady, nil
		}

		return false, nil
	}
}

func NodeVersionMatch(l *zap.Logger, c kubernetes.Interface, nodeName string, version string) wait.ConditionWithContextFunc {
	return func(ctx context.Context) (bool, error) {
		node, err := c.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			l.Warn("error getting node", zap.Error(err))
			return false, nil
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				if condition.Status == corev1.ConditionTrue {
					return strings.HasPrefix(node.Status.NodeInfo.KubeletVersion, version), nil
				}
			}
		}
		return false, nil
	}
}

func GetVersionFromImage(image string) (string, bool) {
	r := regexp.MustCompile(`(?P<version>v[0-9]+\.[0-9]+\.[0-9]+)`)
	matches := r.FindStringSubmatch(image)
	idx := r.SubexpIndex("version")

	if len(matches) < idx {
		return "", false
	}

	return matches[idx], true
}

func GetNode(ctx context.Context, kubeClient *kubernetes.Clientset, nodeName string) *corev1.Node {
	node, err := kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return node
}
