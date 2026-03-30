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
	"bytes"
	"context"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/tabtab-ai/claw-swarm-operator/internal/config"
	k8sclaw "github.com/tabtab-ai/claw-swarm-operator/pkg/claw"
	"github.com/tabtab-ai/claw-swarm-operator/templates"
)

// +kubebuilder:rbac:groups=apps,resources=claws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=claws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=claws/finalizers,verbs=update

const (
	protectIngressClass = "protect-classname"
)

// ClawReconciler reconciles a Claw object
type ClawReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Config config.Config

	stsTpl, svcTpl, ingTpl *template.Template

	idleCount    atomic.Int32
	replenishing atomic.Bool
}

//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services;pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=configuration.konghq.com,resources=kongplugins,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Claw object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *ClawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("reconciling claw")

	sts := appsv1.StatefulSet{}
	if err := r.Get(ctx, req.NamespacedName, &sts); err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("resource not found, triggering replenishment")
			return r.replenishPool(ctx, req.Namespace)
		}

		logger.Error(err, "failed to get StatefulSet")
		return ctrl.Result{}, err
	}

	if sts.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if err := r.reconcileIngress(ctx, &sts); err != nil {
		return ctrl.Result{}, err
	}

	if stopAtStr, ok := sts.Annotations[k8sclaw.ScheduledStopTime]; ok {
		if stopAt, err := time.Parse(time.RFC3339, stopAtStr); err == nil {
			now := time.Now()
			logger.Info("claw has scheduled stop time", "stopTime", stopAtStr, "now", now.Format(time.RFC3339))

			if now.After(stopAt) {
				logger.Info("pausing claw", "stopTime", stopAtStr)
				delete(sts.Annotations, k8sclaw.ScheduledStopTime)
				delete(sts.Annotations, k8sclaw.ScheduledStopTimeTrigger)
				if sts.Spec.Replicas == nil {
					sts.Spec.Replicas = new(int32)
				}
				*sts.Spec.Replicas = 0
				return ctrl.Result{}, r.Update(ctx, &sts)
			}
			logger.Info("claw is before stop time, checking trigger annotation", "requeueAfter", stopAt.Sub(now))
			if _, ok := sts.Annotations[k8sclaw.ScheduledStopTimeTrigger]; !ok {
				sts.Annotations[k8sclaw.ScheduledStopTimeTrigger] = ""
				if err = r.Update(ctx, &sts); err != nil {
					return ctrl.Result{}, err
				}
				logger.Info("trigger annotation added, requeueing", "requeueAfter", stopAt.Sub(now))
				return ctrl.Result{RequeueAfter: stopAt.Sub(now)}, nil
			}
		}
	}

	imageChanged := false
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == k8sclaw.CONTAINER_NAME {
			if sts.Spec.Template.Spec.Containers[i].Image != r.Config.RuntimeImage {
				sts.Spec.Template.Spec.Containers[i].Image = r.Config.RuntimeImage
				imageChanged = true
			}
			break
		}
	}
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == k8sclaw.INIT_CONTAINER_NAME {
			if sts.Spec.Template.Spec.InitContainers[i].Image != r.Config.RuntimeImage {
				sts.Spec.Template.Spec.InitContainers[i].Image = r.Config.RuntimeImage
				imageChanged = true
			}
			break
		}
	}

	if imageChanged {
		shouldUpdate := sts.Labels == nil ||
			sts.Labels[k8sclaw.TAB_CLAW_OCCUPIED] == "" ||
			sts.Spec.Replicas == nil ||
			*sts.Spec.Replicas == 0
		if !shouldUpdate {
			pod := corev1.Pod{}
			err := r.Get(ctx, types.NamespacedName{Namespace: sts.Namespace, Name: sts.Name + "-0"}, &pod)
			shouldUpdate = err != nil || !isPodRunning(&pod)
		}
		if shouldUpdate {
			return ctrl.Result{}, r.Update(ctx, &sts)
		}
	}

	return r.replenishPool(ctx, req.Namespace)
}

func (r *ClawReconciler) replenishPool(ctx context.Context, namespace string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	poolSize := int32(r.Config.PoolSize)

	if !r.replenishing.CompareAndSwap(false, true) {
		logger.V(1).Info("replenishment already in progress, skipping")
		return ctrl.Result{}, nil
	}
	defer r.replenishing.Store(false)

	diff := r.idleCount.Load() - poolSize

	if diff < 0 {
		logger.Info("replenishing pool", "idle", r.idleCount.Load(), "poolSize", poolSize, "toCreate", -diff)
	}
	for ; diff < 0; diff++ {
		if err := r.addFromTemplate(ctx, namespace); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *ClawReconciler) reconcileIngress(ctx context.Context, sts *appsv1.StatefulSet) error {
	logger := log.FromContext(ctx)
	ing := &networkv1.Ingress{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: sts.Name}, ing)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			logger.Error(err, "failed to get ingress for claw")
			return err
		}
		return nil
	}

	if sts.Spec.Replicas == nil || *sts.Spec.Replicas == 0 {
		if ing.Spec.IngressClassName != nil && *ing.Spec.IngressClassName == r.Config.IngressClassName {
			logger.Info("claw paused, switching ingress to protect class")
			ingCopy := ing.DeepCopy()
			*ingCopy.Spec.IngressClassName = protectIngressClass
			return r.Patch(ctx, ingCopy, client.MergeFrom(ing))
		}
		return nil
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != r.Config.IngressClassName {
		ingCopy := ing.DeepCopy()
		*ingCopy.Spec.IngressClassName = r.Config.IngressClassName
		logger.Info("claw resumed, restoring ingress class")
		return r.Patch(ctx, ingCopy, client.MergeFrom(ing))
	}
	return nil
}

func (r *ClawReconciler) beforeStart() error {
	tmpl, err := template.ParseFS(templates.TmplFS, "files/*.yaml")
	if err != nil {
		return err
	}

	r.stsTpl = tmpl.Lookup("claw_statefulset.yaml")
	r.svcTpl = tmpl.Lookup("claw_service.yaml")
	r.ingTpl = tmpl.Lookup("claw_ingress.yaml")

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.beforeStart(); err != nil {
		return err
	}

	clawFilter := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		lbls := obj.GetLabels()
		if lbls == nil {
			return false
		}
		_, hasTrigger := lbls[k8sclaw.TAB_CLAW_INIT_TRIGGER]
		_, hasClaw := lbls[k8sclaw.TAB_CLAW]
		return hasTrigger || hasClaw
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("Claw").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 2,
		}).
		Watches(
			&appsv1.StatefulSet{},
			handler.Funcs{
				CreateFunc: func(ctx context.Context, tce event.CreateEvent, trli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					if _, wasOccupied := tce.Object.GetLabels()[k8sclaw.TAB_CLAW_OCCUPIED]; !wasOccupied {
						r.idleCount.Add(1)
					}

					trli.Add(reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: tce.Object.GetNamespace(),
							Name:      tce.Object.GetName(),
						},
					})
				},
				UpdateFunc: func(ctx context.Context, tue event.UpdateEvent, trli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					_, wasOccupied := tue.ObjectOld.GetLabels()[k8sclaw.TAB_CLAW_OCCUPIED]
					_, isOccupied := tue.ObjectNew.GetLabels()[k8sclaw.TAB_CLAW_OCCUPIED]
					if wasOccupied && !isOccupied {
						// release
						r.idleCount.Add(1)
					} else if isOccupied && !wasOccupied {
						// used
						r.idleCount.Add(-1)
					}

					trli.Add(reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: tue.ObjectNew.GetNamespace(),
							Name:      tue.ObjectNew.GetName(),
						},
					})
				},
				DeleteFunc: func(ctx context.Context, tde event.DeleteEvent, trli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					if _, ok := tde.Object.GetLabels()[k8sclaw.TAB_CLAW_OCCUPIED]; !ok {
						r.idleCount.Add(-1)
					}

					trli.Add(reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: tde.Object.GetNamespace(),
							Name:      tde.Object.GetName(),
						},
					})
				},
			},
			builder.WithPredicates(clawFilter),
		).
		Complete(r)
}

func (r *ClawReconciler) configStatefulset(sts *appsv1.StatefulSet) {
	for i := range len(sts.Spec.Template.Spec.Containers) {
		sts.Spec.Template.Spec.Containers[i].Resources = *r.Config.Resources
	}
	for i := range len(sts.Spec.Template.Spec.InitContainers) {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "init-tabclaw" {
			sts.Spec.Template.Spec.InitContainers[i].Resources = *r.Config.Resources
		}
	}
}

func (r *ClawReconciler) addFromTemplate(ctx context.Context, namespace string) error {
	logger := log.FromContext(ctx)
	clawName, err := k8sclaw.RandomName(8)
	if err != nil {
		logger.Error(err, "failed to generate claw name")
		return err
	}
	logger.V(5).Info("generated claw name", "name", clawName)

	initCfg := r.Config.Init
	env := initCfg.Env
	if len(env) == 0 {
		env = map[string]string{
			"OPENCLAW_GATEWAY_BIND":  "lan",
			"OPENCLAW_WORKSPACE_DIR": "/home/node/.openclaw/workspace",
			"OPENCLAW_CONFIG_DIR":    "/home/node/.openclaw",
		}
	}
	storageSize := initCfg.StorageSize
	if storageSize == "" {
		storageSize = "2Gi"
	}
	gatewayPort := initCfg.Port
	if gatewayPort == 0 {
		gatewayPort = 18789
	}
	gatewayAuth := initCfg.Auth
	if gatewayAuth == "" {
		gatewayAuth = "token"
	}
	gatewayToken := uuid.New().String()

	data := map[string]any{
		"sts_name":           clawName,
		"sts_namespace":      namespace,
		"image":              r.Config.RuntimeImage,
		"storage":            r.Config.StorageClass,
		"storage_size":       storageSize,
		"domain":             r.Config.IngressDomain,
		"host_prefix":        r.Config.IngressHostPrefix,
		"env":                env,
		"gateway_port":       gatewayPort,
		"gateway_auth":       gatewayAuth,
		"gateway_token":      gatewayToken,
		"tls_secret":         r.Config.IngressTlsSecret,
		"ingress_name":       r.Config.IngressClassName,
		"image_pull_secrets": r.Config.ImagePullSecrets,
	}
	if len(r.Config.NodeSelectorValue) > 0 {
		data["node_selector"] = r.Config.NodeSelectorValue
	}

	logger.V(5).Info("rendering claw template", "vars", data)
	stsBuf := bytes.NewBuffer([]byte{})
	if err = r.stsTpl.Execute(stsBuf, data); err != nil {
		logger.Error(err, "failed to execute sts template file")
		return err
	}
	logger.V(5).Info("unmarshaling claw manifest")
	var sts appsv1.StatefulSet
	if err = yaml.Unmarshal(stsBuf.Bytes(), &sts); err != nil {
		logger.Error(err, "failed to unmarshal statefulset")
		return err
	}

	r.configStatefulset(&sts)

	logger.V(5).Info("creating claw", "name", clawName)
	if err = r.Create(ctx, &sts); err != nil {
		logger.Error(err, "failed to create statefulset")
		return err
	}
	defer func() {
		if err != nil {
			if err = r.Delete(ctx, &sts); err != nil {
				logger.Error(err, "failed to clear statefulset")
			}
		}
	}()

	svcBuf := bytes.NewBuffer([]byte{})
	if err = r.svcTpl.Execute(svcBuf, data); err != nil {
		logger.Error(err, "failed to execute svc template file")
		return err
	}

	logger.V(5).Info("unmarshaling service manifest")
	var svc corev1.Service
	if err = yaml.Unmarshal(svcBuf.Bytes(), &svc); err != nil {
		logger.Error(err, "failed to unmarshal service")
		return err
	}
	svc.OwnerReferences = []v1.OwnerReference{
		{
			APIVersion:         "apps/v1",
			Kind:               "StatefulSet",
			Name:               sts.Name,
			UID:                sts.UID,
			BlockOwnerDeletion: ptr.To(true),
		},
	}
	logger.V(5).Info("creating service", "name", clawName)
	if err = r.Create(ctx, &svc); err != nil {
		logger.Error(err, "failed to create service, cleanup statefulset")
		return err
	}

	ingressBuf := bytes.NewBuffer([]byte{})
	if err = r.ingTpl.Execute(ingressBuf, data); err != nil {
		logger.Error(err, "failed to execute ingress template file")
		return err
	}

	logger.V(5).Info("unmarshaling ingress manifest")
	var ing networkv1.Ingress
	if err = yaml.Unmarshal(ingressBuf.Bytes(), &ing); err != nil {
		logger.Error(err, "failed to unmarshal ingress")
		return err
	}
	ing.OwnerReferences = []v1.OwnerReference{
		{
			APIVersion:         "apps/v1",
			Kind:               "StatefulSet",
			Name:               sts.Name,
			UID:                sts.UID,
			BlockOwnerDeletion: ptr.To(true),
		},
	}
	logger.V(5).Info("creating ingress", "name", clawName)
	if err = r.Create(ctx, &ing); err != nil {
		logger.Error(err, "failed to create ingress, cleanup statefulset")
		return err
	}

	logger.V(5).Info("creating secret", "name", clawName)
	secret := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      clawName,
			Namespace: r.Config.WatchNamespace,
			OwnerReferences: []v1.OwnerReference{
				{
					APIVersion:         "apps/v1",
					Kind:               "StatefulSet",
					Name:               sts.Name,
					UID:                sts.UID,
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Immutable: ptr.To(true),
		StringData: map[string]string{
			"token": gatewayToken,
		},
	}
	if err = r.Create(ctx, secret); err != nil {
		logger.Error(err, "failed to create secret")
		return err
	}

	logger.V(1).Info("claw created", "name", clawName)
	return nil
}

func isPodRunning(pod *corev1.Pod) bool {
	index := 0
	for ; index < len(pod.Status.Conditions); index++ {
		if pod.Status.Conditions[index].Type == corev1.PodReady {
			break
		}
	}
	return index != len(pod.Status.Conditions) && pod.Status.Phase == corev1.PodRunning
}
