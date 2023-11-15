/*
Copyright 2023 Red Hat.

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

package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	osv1 "github.com/openshift/api/console/v1"

	openshiftoperatorclientset "github.com/openshift/client-go/operator/clientset/versioned"
	"github.com/openshift/observability-ui-operator/api/v1alpha1"
	consoleplugin "github.com/openshift/observability-ui-operator/internal/consoleplugin"
	"github.com/openshift/observability-ui-operator/internal/observability-ui/common"
	subreconciler "github.com/openshift/observability-ui-operator/internal/subreconciler"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type ObservabilityUIPluginReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	Cfg        *rest.Config
	osopclient openshiftoperatorclientset.Interface
}

const clusterConsole = "cluster"

// +kubebuilder:rbac:groups=observability-ui.openshift.io.openshift.io,resources=observabilityuiplugins,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=observability-ui.openshift.io.openshift.io,resources=observabilityuiplugins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=observability-ui.openshift.io.openshift.io,resources=observabilityuiplugins/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
func (r *ObservabilityUIPluginReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)

	subreconcilersForObservabilityUI := []subreconciler.FnWithRequest{
		r.setStatusToUnknown,
		r.addFinalizer,
		r.handleDelete,
		r.reconcileDeployment,
		r.reconcileService,
		r.reconcileConsolePlugin,
		r.registerPluginInConsole,
		r.updateStatus,
	}

	osopclient, err := openshiftoperatorclientset.NewForConfig(r.Cfg)
	if err != nil {
		return ctrl.Result{Requeue: false}, errors.Errorf("creating openshift operator client")
	}
	r.osopclient = osopclient

	for _, f := range subreconcilersForObservabilityUI {
		if r, err := f(ctx, req); subreconciler.ShouldHaltOrRequeue(r, err) {
			return subreconciler.Evaluate(r, err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *ObservabilityUIPluginReconciler) updateStatus(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
		Status: metav1.ConditionTrue, Reason: "Reconciling",
		Message: fmt.Sprintf("Resources for custom resource (%s) created successfully", obsUIPlugin.Name)})

	if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
		log.Error(err, "Failed to update ObservailityUIPlugin status")
		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) getLatestObsUI(ctx context.Context, req ctrl.Request, obsUIPlugin *v1alpha1.ObservabilityUIPlugin) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)
	if err := r.Get(ctx, req.NamespacedName, obsUIPlugin); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "ObservabilityUIPlugin resource not found. Ignoring since object must be deleted")
			return subreconciler.DoNotRequeue()
		}
		log.Error(err, "failed to get ObservabilityUI")
		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) setStatusToUnknown(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	if obsUIPlugin.Status.Conditions == nil || len(obsUIPlugin.Status.Conditions) == 0 {
		meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
			log.Error(err, "failed to update ObservabilityUI status")
			return subreconciler.RequeueWithError(err)
		}
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) addFinalizer(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	if !controllerutil.ContainsFinalizer(obsUIPlugin, common.ObservabilityUIFinalizer) {
		log.Info("adding finalizer for ObservabilityUIPlugin")
		if ok := controllerutil.AddFinalizer(obsUIPlugin, common.ObservabilityUIFinalizer); !ok {
			log.Error(nil, "failed to add finalizer into the custom resource")
			return subreconciler.Requeue()
		}

		if err := r.Update(ctx, obsUIPlugin); err != nil {
			log.Error(err, "failed to update custom resource to add finalizer")
			return subreconciler.RequeueWithError(err)
		}
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) handleDelete(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	isObsUIPluginMarkedToBeDeleted := obsUIPlugin.GetDeletionTimestamp() != nil
	if isObsUIPluginMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(obsUIPlugin, common.ObservabilityUIFinalizer) {
			log.Info("performing finalizer operations for ObservabilityUIPlugin before delete custom resource")

			meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeDegradedObservabilityUI,
				Status: metav1.ConditionUnknown, Reason: "Finalizing",
				Message: fmt.Sprintf("performing finalizer operations for the custom resource: %s ", obsUIPlugin.Name)})

			if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
				log.Error(err, "failed to update ObservabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			// Perform all operations required before remove the finalizer and allow
			// the Kubernetes API to remove the custom resource.
			r.doFinalizerOperationsForObservabilityUI(ctx, req, obsUIPlugin)

			// TODO(user): If you add operations to the doFinalizerOperationsForObservabilityUI method
			// then you need to ensure that all worked fine before deleting and updating the Downgrade status
			// otherwise, you should requeue here.

			// Re-fetch the ObservabilityUI Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, obsUIPlugin); err != nil {
				log.Error(err, "failed to re-fetch ObservabilityUIPlugin")
				return subreconciler.RequeueWithError(err)
			}

			meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeDegradedObservabilityUI,
				Status: metav1.ConditionTrue, Reason: "Finalizing",
				Message: fmt.Sprintf("finalizer operations for custom resource %s name were successfully accomplished", obsUIPlugin.Name)})

			if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
				log.Error(err, "failed to update ObservabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			log.Info("removing finalizer for ObservabilityUI after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(obsUIPlugin, common.ObservabilityUIFinalizer); !ok {
				log.Error(nil, "failed to remove finalizer for ObservabilityUIPLugin")
				return subreconciler.Requeue()
			}

			if err := r.Update(ctx, obsUIPlugin); err != nil {
				log.Error(err, "failed to remove finalizer for ObservabilityUIPlugin")
				return subreconciler.RequeueWithError(err)
			}
		}

		return subreconciler.DoNotRequeue()
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) reconcileDeployment(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUIPlugin(obsUIPlugin, defaultNamespace)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource deployment (%s)", obsUIPlugin.Name))
		return subreconciler.RequeueWithError(err)
	}

	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.DeploymentName(), Namespace: defaultNamespace}, found)

	if err != nil && apierrors.IsNotFound(err) {
		dep := builder.GetDeployment()

		if err := ctrl.SetControllerReference(obsUIPlugin, dep, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for plugin deployment")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new deployment: namespace %s name %s", dep.Namespace, dep.Name))
		if err = r.Create(ctx, dep); err != nil {
			log.Error(err, fmt.Sprintf("failed to create new deployment: namespace %s name %s", dep.Namespace, dep.Name))

			meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create deployment for (%s): (%s)", obsUIPlugin.Name, err)})

			if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
				log.Error(err, "failed to update observabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			return subreconciler.RequeueWithError(err)
		}
	} else if err != nil {
		log.Error(err, "failed to get Deployment")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) reconcileService(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUIPlugin(obsUIPlugin, defaultNamespace)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource service (%s)", obsUIPlugin.Name))
		return subreconciler.RequeueWithError(err)
	}

	found := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.ServiceName(), Namespace: defaultNamespace}, found)

	if err != nil && apierrors.IsNotFound(err) {
		ser := builder.GetService()

		if err := ctrl.SetControllerReference(obsUIPlugin, ser, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for plugin service")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new service: namespace %s name %s", ser.Namespace, ser.Name))
		if err = r.Create(ctx, ser); err != nil {
			log.Error(err, fmt.Sprintf("failed to create new service: namespace %s name %s", ser.Namespace, ser.Name))

			meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create service for the custom resource %s: %s", obsUIPlugin.Name, err)})

			if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
				log.Error(err, "failed to update ObservabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			return subreconciler.RequeueWithError(err)
		}
	} else if err != nil {
		log.Error(err, "failed to get service")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) registerPluginInConsole(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	builder, err := consoleplugin.FromObsUIPlugin(obsUIPlugin, common.GetObservabilityUINamespace())
	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource console plugin (%s)", obsUIPlugin.Name))
		return subreconciler.RequeueWithError(err)
	}

	// patch console operator to enable the plugin
	// NOTE: Remove this step once the Red Hat signed plugins can be enabled by default

	consoleClient := r.osopclient.OperatorV1().Consoles()

	console, err := consoleClient.Get(ctx, clusterConsole, metav1.GetOptions{})
	if err != nil {
		log.Error(err, fmt.Sprintf("retrieving console %s failed", clusterConsole))
		return subreconciler.RequeueWithDelayAndError(time.Minute, err)
	}

	if slices.Contains(console.Spec.Plugins, builder.ConsolePluginName()) {
		log.Info(fmt.Sprintf("console already contains plugin %s", builder.ConsolePluginName()))
		return subreconciler.ContinueReconciling()
	}

	var patches []jsonPatch

	if console.Spec.Plugins == nil {
		patches = []jsonPatch{{
			Op:    "add",
			Path:  "/spec/plugins",
			Value: []string{builder.ConsolePluginName()},
		}}
	} else {
		patches = []jsonPatch{{
			Op:    "add",
			Path:  "/spec/plugins/-",
			Value: builder.ConsolePluginName(),
		}}
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		log.Error(err, "failed to marshal the console patch while enabling the dynamic plugin")
		return subreconciler.DoNotRequeue()
	}

	log.Info(fmt.Sprintf("enabling plugin %s in console %s", obsUIPlugin.Name, clusterConsole))
	_, err = consoleClient.Patch(ctx, clusterConsole, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
			Status: metav1.ConditionFalse, Reason: "Reconciling",
			Message: fmt.Sprintf("failed to register console-plugin %s with console %s: %s", obsUIPlugin.Name, clusterConsole, err)})

		if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
			log.Error(err, "failed to update observabilityUIPlugin status")
			return subreconciler.RequeueWithError(err)
		}

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) reconcileConsolePlugin(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx, "module", "observability-ui-plugin", "reconcileConsolePlugin", req.NamespacedName)

	obsUIPlugin := &v1alpha1.ObservabilityUIPlugin{}

	if r, err := r.getLatestObsUI(ctx, req, obsUIPlugin); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUIPlugin(obsUIPlugin, defaultNamespace)
	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource console plugin (%s)", obsUIPlugin.Name))
		return subreconciler.RequeueWithError(err)
	}

	// TODO: check if the cluster does not support console plugin v1

	found := &osv1.ConsolePlugin{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.ConsolePluginName()}, found)

	if err != nil && apierrors.IsNotFound(err) {
		log.Info(fmt.Sprintf("creating a new console plugin: %s", builder.ConsolePluginName()))
		cp := builder.GetConsolePluginV1()
		log.Info(fmt.Sprintf("%v", builder))

		log.Info(fmt.Sprintf("%v", cp.Spec.Proxy))

		if err := ctrl.SetControllerReference(obsUIPlugin, cp, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for console plugin")
			return subreconciler.RequeueWithError(err)
		}

		if err = r.Create(ctx, cp); err != nil {
			meta.SetStatusCondition(&obsUIPlugin.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create Builder for the custom resource console plugin %s: %s", obsUIPlugin.Name, err)})

			if err := r.Status().Update(ctx, obsUIPlugin); err != nil {
				log.Error(err, "failed to update observabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			log.Error(err, fmt.Sprintf("failed to create new console plugin: %s", cp.Name))
			return subreconciler.RequeueWithError(err)
		}
	} else if err != nil {
		log.Error(err, "failed to get console plugin")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIPluginReconciler) doFinalizerOperationsForObservabilityUI(ctx context.Context, req ctrl.Request, obsUIPlugin *v1alpha1.ObservabilityUIPlugin) {
	log := log.FromContext(ctx, "module", "observability-ui-plugin", "reconcileConsolePlugin", req.NamespacedName)

	log.WithValues("observability-ui", req.NamespacedName)

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUIPlugin(obsUIPlugin, defaultNamespace)

	if r.Recorder != nil {
		r.Recorder.Event(obsUIPlugin, "Warning", "Deleting",
			fmt.Sprintf("custom resource %s is being deleted", obsUIPlugin.Name))
	}

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource finalizer (%s)", obsUIPlugin.Name))
		return
	}

	consoleClient := r.osopclient.OperatorV1().Consoles()

	console, err := consoleClient.Get(ctx, clusterConsole, metav1.GetOptions{})
	if err != nil {
		log.Error(err, fmt.Sprintf("retrieving console %s failed", clusterConsole))
		return
	}

	index := slices.Index(console.Spec.Plugins, builder.ConsolePluginName())

	if index == -1 {
		log.Info(fmt.Sprintf("console does not contain plugin %s", builder.ConsolePluginName()))
		return
	}

	var patches []jsonPatch

	if console.Spec.Plugins != nil {
		patches = []jsonPatch{{
			Op:   "remove",
			Path: fmt.Sprintf("/spec/plugins/%d", index),
		}}
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		log.Error(err, "failed to marshal the console patch while disabling the dynamic plugin")
		return
	}

	log.Info(fmt.Sprintf("disabling plugin %s in console %s", builder.ConsolePluginName(), clusterConsole))
	_, err = consoleClient.Patch(ctx, clusterConsole, types.JSONPatchType, patchBytes, metav1.PatchOptions{})

	if err != nil {
		log.Info(fmt.Sprintf("console plugin not found for %s", obsUIPlugin.Name))
		return
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ObservabilityUIPluginReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ObservabilityUIPlugin{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

type jsonPatch struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}
