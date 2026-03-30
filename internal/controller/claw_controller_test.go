/*
Copyright 2026.

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
	"fmt"
	"maps"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tabtab-ai/claw-swarm-operator/internal/config"
	k8sclaw "github.com/tabtab-ai/claw-swarm-operator/pkg/claw"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestReconciler(poolSize int) *ClawReconciler {
	cfg := config.DefaultConfig()
	cfg.IngressDomain = "example.com"
	cfg.IngressClassName = "nginx"
	cfg.WatchNamespace = "default"
	cfg.PoolSize = poolSize

	r := &ClawReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Config: cfg,
	}
	ExpectWithOffset(1, r.beforeStart()).To(Succeed())
	return r
}

// newFakeReconciler creates a reconciler backed by a fake client with optional
// interceptors, useful for injecting errors.
func newFakeReconciler(poolSize int, funcs interceptor.Funcs) *ClawReconciler {
	fc := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithInterceptorFuncs(funcs).
		Build()

	cfg := config.DefaultConfig()
	cfg.IngressDomain = "example.com"
	cfg.IngressClassName = "nginx"
	cfg.WatchNamespace = "default"
	cfg.PoolSize = poolSize

	r := &ClawReconciler{
		Client: fc,
		Scheme: scheme.Scheme,
		Config: cfg,
	}
	ExpectWithOffset(1, r.beforeStart()).To(Succeed())
	return r
}

func minimalSTS(name, namespace string, labels map[string]string) *appsv1.StatefulSet {
	lbls := map[string]string{
		k8sclaw.TAB_CLAW:      "true",
		k8sclaw.TAB_CLAW_NAME: name,
	}
	maps.Copy(lbls, labels)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    lbls,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{k8sclaw.TAB_CLAW_NAME: name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{k8sclaw.TAB_CLAW_NAME: name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "placeholder", Image: "nginx"},
					},
				},
			},
		},
	}
}

func cleanupClaws() {
	stsList := &appsv1.StatefulSetList{}
	_ = k8sClient.List(ctx, stsList, client.InNamespace("default"),
		client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})
	for i := range stsList.Items {
		_ = k8sClient.Delete(ctx, &stsList.Items[i])
	}
}

func reconcileReq(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

var _ = Describe("Claw Controller", func() {

	AfterEach(func() {
		cleanupClaws()
	})

	// -------------------------------------------------------------------------
	// Pool replenishment
	// -------------------------------------------------------------------------

	Context("Pool replenishment", func() {
		It("creates instances to fill the pool when idle count is below pool size", func() {
			const poolSize = 2
			r := newTestReconciler(poolSize)

			_, err := r.Reconcile(ctx, reconcileReq("default", "nonexistent-trigger"))
			Expect(err).NotTo(HaveOccurred())

			stsList := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, stsList, client.InNamespace("default"),
				client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})).To(Succeed())
			Expect(stsList.Items).To(HaveLen(poolSize))
		})

		It("skips when replenishment is already in progress", func() {
			r := newTestReconciler(2)
			r.replenishing.Store(true)

			_, err := r.Reconcile(ctx, reconcileReq("default", "nonexistent"))
			Expect(err).NotTo(HaveOccurred())

			stsList := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, stsList, client.InNamespace("default"),
				client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})).To(Succeed())
			Expect(stsList.Items).To(BeEmpty())
		})

		It("returns error when addFromTemplate fails", func() {
			r := newFakeReconciler(1, interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return fmt.Errorf("injected create error")
				},
			})

			_, err := r.Reconcile(ctx, reconcileReq("default", "nonexistent"))
			Expect(err).To(HaveOccurred())
		})
	})

	// -------------------------------------------------------------------------
	// DeletionTimestamp
	// -------------------------------------------------------------------------

	Context("DeletionTimestamp", func() {
		It("returns immediately without error when STS has a deletion timestamp", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("sts-deleting", "default", nil)
			sts.Finalizers = []string{"test/keep-alive"}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			Expect(k8sClient.Delete(ctx, sts)).To(Succeed())

			// Reload: DeletionTimestamp is now set but the object still exists
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, sts)).To(Succeed())
			Expect(sts.DeletionTimestamp).NotTo(BeNil())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			// Remove finalizer so the envtest cluster can clean it up
			sts.Finalizers = nil
			_ = k8sClient.Update(ctx, sts)
		})
	})

	// -------------------------------------------------------------------------
	// Scheduled pause
	// -------------------------------------------------------------------------

	Context("Scheduled pause", func() {
		It("sets replicas to 0 when stop time has already passed", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-pause-past", "default", nil)
			sts.Annotations = map[string]string{
				k8sclaw.ScheduledStopTime: time.Now().Add(-time.Minute).Format(time.RFC3339),
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Replicas).NotTo(BeNil())
			Expect(*updated.Spec.Replicas).To(Equal(int32(0)))
		})

		It("adds trigger annotation and requeues when stop time is in the future", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-pause-future", "default", nil)
			sts.Annotations = map[string]string{
				k8sclaw.ScheduledStopTime: time.Now().Add(time.Hour).Format(time.RFC3339),
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			updated := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, updated)).To(Succeed())
			_, hasTrigger := updated.Annotations[k8sclaw.ScheduledStopTimeTrigger]
			Expect(hasTrigger).To(BeTrue())
		})

		It("falls through to pool check when trigger annotation is already set", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-pause-triggered", "default", nil)
			sts.Annotations = map[string]string{
				k8sclaw.ScheduledStopTime:        time.Now().Add(time.Hour).Format(time.RFC3339),
				k8sclaw.ScheduledStopTimeTrigger: "",
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Should not error and should not requeue with a delay
			result, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
		})
	})

	// -------------------------------------------------------------------------
	// Image updates
	// -------------------------------------------------------------------------

	Context("Image update", func() {
		It("updates container image for idle instances", func() {
			r := newTestReconciler(0)
			r.Config.RuntimeImage = "new-image:v2"

			sts := minimalSTS("claw-img-idle", "default", nil)
			sts.Spec.Template.Spec.Containers = []corev1.Container{
				{Name: k8sclaw.CONTAINER_NAME, Image: "old-image:v1"},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal("new-image:v2"))
		})

		It("updates init container image for idle instances", func() {
			r := newTestReconciler(0)
			r.Config.RuntimeImage = "new-image:v2"

			sts := minimalSTS("claw-img-init", "default", nil)
			sts.Spec.Template.Spec.InitContainers = []corev1.Container{
				{Name: k8sclaw.INIT_CONTAINER_NAME, Image: "old-image:v1"},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Template.Spec.InitContainers[0].Image).To(Equal("new-image:v2"))
		})

		It("skips image update when occupied instance has a running pod", func() {
			r := newTestReconciler(0)
			r.Config.RuntimeImage = "new-image:v2"

			sts := minimalSTS("claw-img-skip", "default",
				map[string]string{k8sclaw.TAB_CLAW_OCCUPIED: "true"})
			sts.Spec.Template.Spec.Containers = []corev1.Container{
				{Name: k8sclaw.CONTAINER_NAME, Image: "old-image:v1"},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Create a Running pod so the controller sees it as active
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sts.Name + "-0",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "nginx"}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status = corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal("old-image:v1"),
				"image must not change for an occupied, running instance")
		})

		It("updates image when occupied but pod is not running", func() {
			r := newTestReconciler(0)
			r.Config.RuntimeImage = "new-image:v2"

			sts := minimalSTS("claw-img-norun", "default",
				map[string]string{k8sclaw.TAB_CLAW_OCCUPIED: "true"})
			sts.Spec.Template.Spec.Containers = []corev1.Container{
				{Name: k8sclaw.CONTAINER_NAME, Image: "old-image:v1"},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Pod exists but is Pending, not Running
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sts.Name + "-0",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "nginx"}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal("new-image:v2"))
		})
	})

	// -------------------------------------------------------------------------
	// reconcileIngress
	// -------------------------------------------------------------------------

	Context("reconcileIngress", func() {
		createIngress := func(name, ingressClass string) {
			ing := &networkv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: networkv1.IngressSpec{
					IngressClassName: ptr.To(ingressClass),
					Rules: []networkv1.IngressRule{
						{Host: name + ".example.com"},
					},
				},
			}
			ExpectWithOffset(1, k8sClient.Create(ctx, ing)).To(Succeed())
		}

		AfterEach(func() {
			ingList := &networkv1.IngressList{}
			_ = k8sClient.List(ctx, ingList, client.InNamespace("default"))
			for i := range ingList.Items {
				_ = k8sClient.Delete(ctx, &ingList.Items[i])
			}
		})

		It("switches ingress to protect class when instance is paused", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-ing-pause", "default", nil)
			sts.Spec.Replicas = ptr.To(int32(0))
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			createIngress(sts.Name, "nginx")

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			ing := &networkv1.Ingress{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, ing)).To(Succeed())
			Expect(ptr.Deref(ing.Spec.IngressClassName, "")).To(Equal(protectIngressClass))
		})

		It("no-ops when paused and ingress class is already protect", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-ing-already-prot", "default", nil)
			sts.Spec.Replicas = ptr.To(int32(0))
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			createIngress(sts.Name, protectIngressClass)

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			ing := &networkv1.Ingress{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, ing)).To(Succeed())
			Expect(ptr.Deref(ing.Spec.IngressClassName, "")).To(Equal(protectIngressClass))
		})

		It("restores configured ingress class when instance is resumed", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-ing-resume", "default", nil)
			sts.Spec.Replicas = ptr.To(int32(1))
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			createIngress(sts.Name, protectIngressClass)

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			ing := &networkv1.Ingress{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, ing)).To(Succeed())
			Expect(ptr.Deref(ing.Spec.IngressClassName, "")).To(Equal("nginx"))
		})

		It("no-ops when running and ingress class is already correct", func() {
			r := newTestReconciler(0)

			sts := minimalSTS("claw-ing-correct", "default", nil)
			sts.Spec.Replicas = ptr.To(int32(1))
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			createIngress(sts.Name, "nginx")

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).NotTo(HaveOccurred())

			ing := &networkv1.Ingress{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: "default", Name: sts.Name}, ing)).To(Succeed())
			Expect(ptr.Deref(ing.Spec.IngressClassName, "")).To(Equal("nginx"))
		})

		It("returns error when ingress Get fails with a non-NotFound error", func() {
			r := newFakeReconciler(0, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*networkv1.Ingress); ok {
						return fmt.Errorf("injected ingress get error")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})

			// Pre-populate a fake STS so Reconcile reaches reconcileIngress
			sts := minimalSTS("claw-ing-err", "default", nil)
			Expect(r.Client.Create(ctx, sts)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcileReq("default", sts.Name))
			Expect(err).To(HaveOccurred())
		})
	})

	// -------------------------------------------------------------------------
	// addFromTemplate variations
	// -------------------------------------------------------------------------

	Context("addFromTemplate", func() {
		It("uses default env, storageSize, port and auth when Init fields are zero", func() {
			r := newTestReconciler(1)
			r.Config.Init.Env = nil
			r.Config.Init.StorageSize = ""
			r.Config.Init.Port = 0
			r.Config.Init.Auth = ""

			_, err := r.Reconcile(ctx, reconcileReq("default", "nonexistent"))
			Expect(err).NotTo(HaveOccurred())

			stsList := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, stsList, client.InNamespace("default"),
				client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})).To(Succeed())
			Expect(stsList.Items).To(HaveLen(1))
		})

		It("sets NodeSelector on the pod template when NodeSelectorValue is configured", func() {
			r := newTestReconciler(1)
			r.Config.NodeSelectorValue = map[string]string{"disk": "ssd"}

			_, err := r.Reconcile(ctx, reconcileReq("default", "nonexistent"))
			Expect(err).NotTo(HaveOccurred())

			stsList := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, stsList, client.InNamespace("default"),
				client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})).To(Succeed())
			Expect(stsList.Items).To(HaveLen(1))
			Expect(stsList.Items[0].Spec.Template.Spec.NodeSelector).
				To(Equal(map[string]string{"disk": "ssd"}))
		})

		It("injects ImagePullSecrets into the pod template", func() {
			r := newTestReconciler(1)
			r.Config.ImagePullSecrets = []string{"my-registry-secret"}

			_, err := r.Reconcile(ctx, reconcileReq("default", "nonexistent"))
			Expect(err).NotTo(HaveOccurred())

			stsList := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, stsList, client.InNamespace("default"),
				client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})).To(Succeed())
			Expect(stsList.Items).To(HaveLen(1))
			Expect(stsList.Items[0].Spec.Template.Spec.ImagePullSecrets).
				To(ContainElement(corev1.LocalObjectReference{Name: "my-registry-secret"}))
		})
	})

	// -------------------------------------------------------------------------
	// isPodRunning
	// -------------------------------------------------------------------------

	Context("isPodRunning", func() {
		It("returns false for a pod with no conditions", func() {
			pod := &corev1.Pod{}
			Expect(isPodRunning(pod)).To(BeFalse())
		})

		It("returns false for a pod whose conditions do not include PodReady", func() {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(isPodRunning(pod)).To(BeFalse())
		})

		It("returns true for a Running pod with PodReady condition", func() {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(isPodRunning(pod)).To(BeTrue())
		})

		It("returns false when PodReady condition exists but pod is not Running", func() {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			}
			Expect(isPodRunning(pod)).To(BeFalse())
		})
	})

	// -------------------------------------------------------------------------
	// SetupWithManager — event handler closures
	// -------------------------------------------------------------------------

	Context("SetupWithManager", Ordered, func() {
		const mgrNs = "mgr-test"

		var (
			r         *ClawReconciler
			mgrCancel context.CancelFunc
		)

		BeforeAll(func() {
			// Ensure the test namespace exists
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mgrNs}}
			_ = k8sClient.Create(ctx, ns)

			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme: scheme.Scheme,
				// Restrict the cache to only the test namespace so the manager
				// does not interfere with other tests running in "default".
				Cache: cache.Options{
					DefaultNamespaces: map[string]cache.Config{mgrNs: {}},
				},
				// Disable metrics and health-probe servers to avoid port conflicts
				// when the test suite runs multiple times or in parallel.
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			})
			Expect(err).NotTo(HaveOccurred())

			r = &ClawReconciler{
				Client: mgr.GetClient(),
				Scheme: mgr.GetScheme(),
				Config: func() config.Config {
					c := config.DefaultConfig()
					c.IngressDomain = "example.com"
					c.IngressClassName = "nginx"
					c.WatchNamespace = mgrNs
					c.PoolSize = 0 // no auto-replenish during this test
					return c
				}(),
			}
			Expect(r.SetupWithManager(mgr)).To(Succeed())

			mgrCtx, cancel := context.WithCancel(ctx)
			mgrCancel = cancel
			go func() { _ = mgr.Start(mgrCtx) }()

			// Wait for the cache to sync before any test creates resources.
			Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
		})

		AfterAll(func() {
			mgrCancel()
			stsList := &appsv1.StatefulSetList{}
			_ = k8sClient.List(ctx, stsList, client.InNamespace(mgrNs),
				client.MatchingLabels{k8sclaw.TAB_CLAW: "true"})
			for i := range stsList.Items {
				_ = k8sClient.Delete(ctx, &stsList.Items[i])
			}
		})

		It("increments idleCount when an idle STS is created (CreateFunc)", func() {
			before := r.idleCount.Load()
			sts := minimalSTS("ev-idle-create", mgrNs, nil)
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			Eventually(func() int32 { return r.idleCount.Load() }).
				Should(Equal(before + 1))
		})

		It("does not change idleCount when an occupied STS is created (CreateFunc)", func() {
			before := r.idleCount.Load()
			sts := minimalSTS("ev-occ-create", mgrNs,
				map[string]string{k8sclaw.TAB_CLAW_OCCUPIED: "true"})
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			Consistently(func() int32 { return r.idleCount.Load() }, "500ms").
				Should(Equal(before))
		})

		It("decrements idleCount when idle STS is allocated (UpdateFunc)", func() {
			sts := minimalSTS("ev-allocate", mgrNs, nil)
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			beforeAlloc := r.idleCount.Load()
			Eventually(func() int32 { return r.idleCount.Load() }).
				Should(BeNumerically(">=", beforeAlloc))

			// Allocate
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: mgrNs, Name: sts.Name}, sts)).To(Succeed())
			sts.Labels[k8sclaw.TAB_CLAW_OCCUPIED] = "true"
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())

			afterAlloc := r.idleCount.Load()
			Eventually(func() int32 { return r.idleCount.Load() }).
				Should(BeNumerically("<=", afterAlloc))
		})

		It("increments idleCount when occupied STS is released (UpdateFunc)", func() {
			sts := minimalSTS("ev-release", mgrNs,
				map[string]string{k8sclaw.TAB_CLAW_OCCUPIED: "true"})
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			before := r.idleCount.Load()

			// Release
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Namespace: mgrNs, Name: sts.Name}, sts)).To(Succeed())
			delete(sts.Labels, k8sclaw.TAB_CLAW_OCCUPIED)
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())

			Eventually(func() int32 { return r.idleCount.Load() }).
				Should(BeNumerically(">", before))
		})

		It("decrements idleCount when an idle STS is deleted (DeleteFunc)", func() {
			sts := minimalSTS("ev-delete-idle", mgrNs, nil)
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			Eventually(func() int32 { return r.idleCount.Load() }).
				Should(BeNumerically(">", 0))

			before := r.idleCount.Load()
			Expect(k8sClient.Delete(ctx, sts)).To(Succeed())
			Eventually(func() int32 { return r.idleCount.Load() }).
				Should(BeNumerically("<", before))
		})

		It("does not decrement idleCount when an occupied STS is deleted (DeleteFunc)", func() {
			sts := minimalSTS("ev-delete-occ", mgrNs,
				map[string]string{k8sclaw.TAB_CLAW_OCCUPIED: "true"})
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Give the manager a moment to process the create event
			time.Sleep(200 * time.Millisecond)
			before := r.idleCount.Load()

			Expect(k8sClient.Delete(ctx, sts)).To(Succeed())
			Consistently(func() int32 { return r.idleCount.Load() }, "500ms").
				Should(Equal(before))
		})
	})
})
