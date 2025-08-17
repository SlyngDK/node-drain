/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"time"

	"github.com/slyngdk/node-drain/internal/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	drainv1 "github.com/slyngdk/node-drain/api/v1"
)

var _ = Describe("Node Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}

		_, err := config.LoadConfig()
		Expect(err).NotTo(HaveOccurred())

		_, _ = config.GetLogger("info", "json")

		BeforeEach(func() {

		})

		AfterEach(func() {
			resource := &drainv1.Node{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			if !apierrors.IsNotFound(err) {
				By("Cleanup the specific resource instance Node")
				controllerutil.RemoveFinalizer(resource, nodeDrainFinalizer)
				Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			kubeNode := &corev1.Node{}
			err = k8sClient.Get(ctx, typeNamespacedName, kubeNode)
			Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			if !apierrors.IsNotFound(err) {
				By("Cleanup the specific kubenode")
				Expect(k8sClient.Delete(ctx, kubeNode)).To(Succeed())
			}
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling kube node which should result in a new created node with same name")

			kubeNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: resourceName,
				},
			}
			Expect(k8sClient.Create(ctx, kubeNode)).To(Succeed())

			controllerReconciler, err := NewNodeReconciler(k8sClient, k8sClient.Scheme(), cfg, managerNamespace, "node-test")
			Expect(err).NotTo(HaveOccurred())

			res, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res).ShouldNot(BeNil())
			Expect(res.RequeueAfter).Should(Equal(5 * time.Second))

			resource := &drainv1.Node{}
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(resource.Spec.State).Should(Equal(drainv1.NodeStateActive))
		})
	})
})
