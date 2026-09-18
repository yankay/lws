/*
Copyright 2025 The Kubernetes Authors.
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

package e2e

import (
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	leaderworkerset "sigs.k8s.io/lws/api/leaderworkerset/v1"
	v1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/lws/pkg/schedulerprovider"
	testing "sigs.k8s.io/lws/test/testutils"
	"sigs.k8s.io/lws/test/wrappers"
)

var _ = ginkgo.Describe("leaderWorkerSet e2e gang scheduling tests", func() {
	// Each test runs in a separate namespace.
	var ns *corev1.Namespace
	var lws *leaderworkerset.LeaderWorkerSet

	ginkgo.BeforeEach(func() {
		// Create test namespace before each test.
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-ns-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		// Wait for namespace to exist before proceeding with test.
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Namespace, Name: ns.Name}, ns)
			return err == nil
		}, timeout, interval).Should(gomega.BeTrue())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(testing.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.Context("with volcano gang scheduling enabled", ginkgo.Ordered, func() {
		ginkgo.BeforeAll(func() {
			if schedulerProvider != schedulerprovider.Volcano {
				ginkgo.Skip("Volcano gang scheduling tests require SCHEDULER_PROVIDER=volcano")
			}
		})

		ginkgo.It("Should create PodGroups when LWS is created with LeaderCreated startup policy", func() {
			lws = wrappers.BuildLeaderWorkerSet(ns.Name).
				Replica(2).
				Size(4).
				SchedulerName("volcano").
				StartupPolicy(v1.LeaderCreatedStartupPolicy).
				Obj()

			testing.MustCreateLws(ctx, k8sClient, lws)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			// Verify PodGroups are created with correct spec and owner reference
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 2)
			pods := &corev1.PodList{}
			testing.ExpectValidPods(ctx, k8sClient, lws, pods)
		})

		ginkgo.It("Should create PodGroups when LWS is created with LeaderReady startup policy", func() {
			lws = wrappers.BuildLeaderWorkerSet(ns.Name).
				Replica(2).
				Size(4).
				SchedulerName("volcano").
				StartupPolicy(v1.LeaderReadyStartupPolicy).
				Obj()

			testing.MustCreateLws(ctx, k8sClient, lws)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			// Verify PodGroups are created with correct spec and owner reference
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 2)
			pods := &corev1.PodList{}
			testing.ExpectValidPods(ctx, k8sClient, lws, pods)
		})

		ginkgo.It("Should clean up PodGroups when LWS is deleted", func() {
			lws = wrappers.BuildLeaderWorkerSet(ns.Name).
				Replica(1).
				Size(2).
				SchedulerName("volcano").
				Obj()
			testing.MustCreateLws(ctx, k8sClient, lws)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			// Verify PodGroups are created with correct spec and owner reference
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 1)
			// Delete LWS
			testing.DeleteLWSWithForground(ctx, k8sClient, lws)
			// Verify PodGroups are eventually cleaned up
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 0)
		})

		ginkgo.It("Should recover after a stale PodGroup finishes garbage collection", func() {
			lws = wrappers.BuildLeaderWorkerSet(ns.Name).
				Replica(1).
				Size(2).
				SchedulerName("volcano").
				Obj()
			testing.MustCreateLws(ctx, k8sClient, lws)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 1)

			leaderKey := types.NamespacedName{Namespace: ns.Name, Name: lws.Name + "-0"}
			var oldLeader corev1.Pod
			gomega.Expect(k8sClient.Get(ctx, leaderKey, &oldLeader)).To(gomega.Succeed())
			pgKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      oldLeader.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey],
			}
			var pg volcanov1beta1.PodGroup
			gomega.Expect(k8sClient.Get(ctx, pgKey, &pg)).To(gomega.Succeed())
			oldPGUID := pg.UID
			const finalizer = "test.lws.sigs.k8s.io/hold-podgroup"
			releasePodGroup := func() error {
				return retry.RetryOnConflict(retry.DefaultRetry, func() error {
					var current volcanov1beta1.PodGroup
					if err := k8sClient.Get(ctx, pgKey, &current); err != nil {
						return client.IgnoreNotFound(err)
					}
					if current.UID != oldPGUID || !controllerutil.RemoveFinalizer(&current, finalizer) {
						return nil
					}
					return k8sClient.Update(ctx, &current)
				})
			}
			ginkgo.DeferCleanup(func() {
				gomega.Expect(releasePodGroup()).To(gomega.Succeed())
			})
			gomega.Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				gomega.Expect(k8sClient.Get(ctx, pgKey, &pg)).To(gomega.Succeed())
				controllerutil.AddFinalizer(&pg, finalizer)
				return k8sClient.Update(ctx, &pg)
			})).To(gomega.Succeed())

			ginkgo.By("letting real garbage collection mark the old PodGroup for deletion")
			testing.UpdateReplicaCount(ctx, k8sClient, lws, 0)
			gomega.Eventually(func() bool {
				var pods corev1.PodList
				gomega.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, pgKey, &pg)).To(gomega.Succeed())
				err := k8sClient.Get(ctx, leaderKey, &appsv1.StatefulSet{})
				gomega.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
				return len(pods.Items) == 0 && apierrors.IsNotFound(err) && pg.DeletionTimestamp != nil
			}, timeout, interval).Should(gomega.BeTrue())

			ginkgo.By("creating a replacement leader before the old PodGroup disappears")
			testing.UpdateReplicaCount(ctx, k8sClient, lws, 1)
			var leader corev1.Pod
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, leaderKey, &leader)
				if apierrors.IsNotFound(err) {
					return false
				}
				gomega.Expect(err).To(gomega.Succeed())
				return leader.UID != oldLeader.UID
			}, timeout, interval).Should(gomega.BeTrue())
			leaderUID := leader.UID
			gomega.Expect(leader.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey]).To(gomega.Equal(pgKey.Name))

			// Hold deletion across multiple retries; the stale object must not be adopted.
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, pgKey, &pg)).To(gomega.Succeed())
				g.Expect(pg.UID).To(gomega.Equal(oldPGUID))
				owner := metav1.GetControllerOf(&pg)
				g.Expect(owner).NotTo(gomega.BeNil())
				g.Expect(owner.UID).To(gomega.Equal(oldLeader.UID))
				err := k8sClient.Get(ctx, leaderKey, &appsv1.StatefulSet{})
				g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "workers must wait for a usable PodGroup")
			}, 5*time.Second, interval).Should(gomega.Succeed())

			ginkgo.By("releasing garbage collection and recovering without updating the leader")
			gomega.Expect(releasePodGroup()).To(gomega.Succeed())
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, pgKey, &pg)
				if apierrors.IsNotFound(err) {
					return false
				}
				gomega.Expect(err).To(gomega.Succeed())
				owner := metav1.GetControllerOf(&pg)
				return pg.UID != oldPGUID && pg.DeletionTimestamp == nil &&
					owner != nil && owner.UID == leaderUID
			}, timeout, interval).Should(gomega.BeTrue())
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 1)
			testing.ExpectValidPods(ctx, k8sClient, lws, &corev1.PodList{})
			gomega.Expect(k8sClient.Get(ctx, leaderKey, &leader)).To(gomega.Succeed())
			gomega.Expect(leader.UID).To(gomega.Equal(leaderUID))
		})

		ginkgo.It("Should recreate PodGroups with correct size when LWS is resized", func() {
			lws = wrappers.BuildLeaderWorkerSet(ns.Name).
				Replica(2).
				Size(2).
				SchedulerName("volcano").
				Obj()
			testing.MustCreateLws(ctx, k8sClient, lws)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			// Verify initial PodGroups with size=2
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 2)
			// Resize to size=4
			testing.UpdateSize(ctx, k8sClient, lws, int32(4))
			// Rolling update completes
			testing.ExpectValidLeaderStatefulSet(ctx, k8sClient, lws, 2)
			testing.ExpectValidWorkerStatefulSets(ctx, lws, k8sClient, true)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			// Pods have been recreated with new size
			lwsPods := &corev1.PodList{}
			testing.ExpectValidPods(ctx, k8sClient, lws, lwsPods)
			for _, p := range lwsPods.Items {
				gomega.Expect(testing.CheckAnnotation(p, leaderworkerset.SizeAnnotationKey, strconv.Itoa(4))).To(gomega.Succeed())
			}
			// Verify PodGroups are recreated with new size
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 2)
		})

		ginkgo.It("Should recreate PodGroups when LWS performs rolling update", func() {
			lws = wrappers.BuildLeaderWorkerSet(ns.Name).
				Replica(2).
				Size(3).
				SchedulerName("volcano").
				Obj()
			testing.MustCreateLws(ctx, k8sClient, lws)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 2)
			// Trigger rolling update
			testing.UpdateWorkerTemplate(ctx, k8sClient, lws)
			// Rolling update completes
			testing.ExpectValidLeaderStatefulSet(ctx, k8sClient, lws, 2)
			testing.ExpectValidWorkerStatefulSets(ctx, lws, k8sClient, true)
			testing.ExpectLeaderWorkerSetAvailable(ctx, k8sClient, lws, "All replicas are ready")
			// Verify final state: still 2 PodGroups exist with updated configuration
			// ExpectValidPodGroups automatically uses current revision, ensuring new PodGroups are validated
			testing.ExpectValidPodGroups(ctx, k8sClient, schedulerprovider.Volcano, lws, 2)
		})
	})
})
