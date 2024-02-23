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

package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	osv1alpha1 "github.com/openshift/api/console/v1alpha1"
	"github.com/pkg/errors"

	openshiftoperatorclientset "github.com/openshift/client-go/operator/clientset/versioned"
	consoleplugin "github.com/openshift/observability-ui-operator/internal/consoleplugin"
	common "github.com/openshift/observability-ui-operator/internal/observability-ui/common"
	subreconciler "github.com/openshift/observability-ui-operator/internal/subreconciler"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openshift/observability-ui-operator/api/v1alpha1"
)

type ObservabilityUIReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	Cfg        *rest.Config
	osopclient openshiftoperatorclientset.Interface
}

const clusterConsole = "cluster"
const pluginRole = "observability-ui-plugin-editor"

// +kubebuilder:rbac:groups=observability-ui.openshift.io,resources=observabilityuis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=observability-ui.openshift.io,resources=observabilityuis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=servicesaccounts,verbs=get;list;watch;create;update;patch;delete
func (r *ObservabilityUIReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)

	subreconcilersForObservabilityUI := []subreconciler.FnWithRequest{
		r.setStatusToUnknown,
		r.handleDelete,
		r.reconcileServiceAccount,
		r.reconcileClusterRole,
		r.reconcileClusterRoleBinding,
		r.reconcileService,
		r.reconcileDeployment,
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

func (r *ObservabilityUIReconciler) setStatusToUnknown(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	if obsUI.Status.Conditions == nil || len(obsUI.Status.Conditions) == 0 {
		meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err := r.Status().Update(ctx, obsUI); err != nil {
			log.Error(err, "Failed to update ObservabilityUI status")
			return subreconciler.RequeueWithError(err)
		}
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) reconcileServiceAccount(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUI(obsUI, defaultNamespace)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource service account (%s)", obsUI.Name))
		return subreconciler.RequeueWithError(err)
	}

	found := &corev1.ServiceAccount{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.ConsolePluginName(), Namespace: defaultNamespace}, found)

	if err != nil && apierrors.IsNotFound(err) {
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      builder.ConsolePluginName(),
				Namespace: defaultNamespace,
				Labels: map[string]string{
					"app": builder.ConsolePluginName(),
				},
			},
		}

		if err := ctrl.SetControllerReference(obsUI, sa, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for plugin service account")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new service account: namespace %s name %s", sa.Namespace, sa.Name))

		if err = r.Create(ctx, sa); err != nil {
			log.Error(err, fmt.Sprintf("failed to create new service account: namespace %s name %s", sa.Namespace, sa.Name))

			meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create service account for (%s): (%s)", obsUI.Name, err)})

			if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
				return r, err
			}

			if err := r.Status().Update(ctx, obsUI); err != nil {
				log.Error(err, "failed to update observabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			return subreconciler.RequeueWithError(err)
		}

		if err := ctrl.SetControllerReference(obsUI, sa, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for plugin service account")
			return subreconciler.RequeueWithError(err)
		}
	} else if err != nil {
		log.Error(err, "failed to get service account")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) reconcileClusterRole(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	found := &rbacv1.ClusterRole{}
	err := r.Get(ctx, types.NamespacedName{Name: pluginRole}, found)

	if err != nil && apierrors.IsNotFound(err) {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: pluginRole,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"observability-ui.openshift.io"},
					Verbs:     []string{"list", "get", "watch", "create", "patch", "update", "delete"},
					Resources: []string{"observabilityuiplugins"},
				},
			},
		}

		if err := ctrl.SetControllerReference(obsUI, cr, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for plugin cluster role")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new cluster role: namespace %s name %s", cr.Namespace, cr.Name))

		if err = r.Create(ctx, cr); err != nil {
			log.Error(err, fmt.Sprintf("failed to create new cluser role: namespace %s name %s", cr.Namespace, cr.Name))

			meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create cluster role for (%s): (%s)", obsUI.Name, err)})

			if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
				return r, err
			}

			if err := r.Status().Update(ctx, obsUI); err != nil {
				log.Error(err, "failed to update observabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			return subreconciler.RequeueWithError(err)
		}

	} else if err != nil {
		log.Error(err, "failed to get cluster role")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) reconcileClusterRoleBinding(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUI(obsUI, defaultNamespace)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource deployment (%s)", obsUI.Name))
		return subreconciler.RequeueWithError(err)
	}

	found := &rbacv1.ClusterRoleBinding{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.ConsolePluginName()}, found)

	if err != nil && apierrors.IsNotFound(err) {
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: builder.ConsolePluginName(),
				Labels: map[string]string{
					"app": builder.ConsolePluginName(),
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     pluginRole,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      builder.ConsolePluginName(),
				Namespace: defaultNamespace,
			}},
		}

		if err := ctrl.SetControllerReference(obsUI, crb, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for plugin cluster role binding")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new cluster role binding: namespace %s name %s", crb.Namespace, crb.Name))

		if err = r.Create(ctx, crb); err != nil {
			log.Error(err, fmt.Sprintf("failed to create new cluser role binding: namespace %s name %s", crb.Namespace, crb.Name))

			meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create cluster role binding for (%s): (%s)", obsUI.Name, err)})

			if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
				return r, err
			}

			if err := r.Status().Update(ctx, obsUI); err != nil {
				log.Error(err, "failed to update observabilityUIPlugin status")
				return subreconciler.RequeueWithError(err)
			}

			return subreconciler.RequeueWithError(err)
		}

	} else if err != nil {
		log.Error(err, "failed to get cluster role binding")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) reconcileService(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUI(obsUI, defaultNamespace)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource service (%s)", obsUI.Name))
		return subreconciler.RequeueWithError(err)
	}

	found := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.ServiceName(), Namespace: defaultNamespace}, found)

	if err != nil && apierrors.IsNotFound(err) {
		ser := builder.GetService()

		if err := ctrl.SetControllerReference(obsUI, ser, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for hub plugin service")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new service: namespace %s name %s", ser.Namespace, ser.Name))
		if err = r.Create(ctx, ser); err != nil {
			log.Error(err, fmt.Sprintf("failed to create new service: namespace %s name %s", ser.Namespace, ser.Name))

			meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create service for the custom resource %s: %s", obsUI.Name, err)})

			if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
				return r, err
			}

			if err := r.Status().Update(ctx, obsUI); err != nil {
				log.Error(err, "failed to update ObservabilityUI status")
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

func (r *ObservabilityUIReconciler) handleDelete(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	isObsUIMarkedToBeDeleted := obsUI.GetDeletionTimestamp() != nil
	if isObsUIMarkedToBeDeleted {
		return subreconciler.DoNotRequeue()
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) getLatestObsUI(ctx context.Context, req ctrl.Request, obsUI *v1alpha1.ObservabilityUI) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)
	if err := r.Get(ctx, req.NamespacedName, obsUI); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "ObservabilityUI resource not found. ignoring since object must be deleted")
			return subreconciler.DoNotRequeue()
		}
		log.Error(err, "failed to get ObservabilityUI")
		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) reconcileDeployment(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUI(obsUI, defaultNamespace)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource deployment (%s)", obsUI.Name))
		return subreconciler.RequeueWithError(err)
	}

	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.DeploymentName(), Namespace: defaultNamespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		dep := builder.GetDeployment()

		if err := ctrl.SetControllerReference(obsUI, dep, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for hub plugin deployment")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new deployment: namespace %s name %s", dep.Namespace, dep.Name))

		if err = r.Create(ctx, dep); err != nil {
			log.Error(err, "failed to define new deployment resource for ObservabilityUI")

			meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create deployment for the custom resource (%s): (%s)", obsUI.Name, err)})

			if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
				return r, err
			}

			if err := r.Status().Update(ctx, obsUI); err != nil {
				log.Error(err, "failed to update ObservabilityUI status")
				return subreconciler.RequeueWithError(err)
			}

			return subreconciler.RequeueWithError(err)
		}
	} else if err != nil {
		log.Error(err, "failed to get deployment")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) reconcileConsolePlugin(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx, "module", "observability-ui", "reconcileConsolePlugin", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUI(obsUI, defaultNamespace)
	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for the custom resource console plugin (%s)", obsUI.Name))
		return subreconciler.RequeueWithError(err)
	}

	// TODO: check if the cluster does not support console plugin v1

	found := &osv1alpha1.ConsolePlugin{}
	err = r.Get(ctx, types.NamespacedName{Name: builder.ConsolePluginName()}, found)

	if err != nil && apierrors.IsNotFound(err) {
		cp := builder.GetConsolePluginV1()

		if err := ctrl.SetControllerReference(obsUI, cp, r.Scheme); err != nil {
			log.Error(err, "failed to set controller reference for hub console plugin")
			return subreconciler.RequeueWithError(err)
		}

		log.Info(fmt.Sprintf("creating a new console plugin: %s", cp.Name))
		if err = r.Create(ctx, cp); err != nil {
			meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("failed to create the console plugin %s: %s", obsUI.Name, err)})

			if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
				return r, err
			}

			if err := r.Status().Update(ctx, obsUI); err != nil {
				log.Error(err, "failed to update observabilityUI status")
				return subreconciler.RequeueWithError(err)
			}
		}
	} else if err != nil {
		log.Error(err, "failed to get console plugin")

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) registerPluginInConsole(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("observability-ui-plugin", req.NamespacedName)

	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	defaultNamespace := common.GetObservabilityUINamespace()

	builder, err := consoleplugin.FromObsUI(obsUI, defaultNamespace)
	if err != nil {
		log.Error(err, fmt.Sprintf("failed to create builder for registering the console plugin for (%s)", obsUI.Name))
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

	log.Info(fmt.Sprintf("enabling plugin %s in console %s", builder.ConsolePluginName(), clusterConsole))
	_, err = consoleClient.Patch(ctx, clusterConsole, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
			Status: metav1.ConditionFalse, Reason: "Reconciling",
			Message: fmt.Sprintf("failed to register console-plugin %s with console %s: %s", obsUI.Name, clusterConsole, err)})

		if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
			return r, err
		}

		if err := r.Status().Update(ctx, obsUI); err != nil {
			log.Error(err, "failed to update observabilityUI status")
			return subreconciler.RequeueWithError(err)
		}

		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.ContinueReconciling()
}

func (r *ObservabilityUIReconciler) updateStatus(ctx context.Context, req ctrl.Request) (*ctrl.Result, error) {
	log := log.FromContext(ctx)
	obsUI := &v1alpha1.ObservabilityUI{}

	if r, err := r.getLatestObsUI(ctx, req, obsUI); subreconciler.ShouldHaltOrRequeue(r, err) {
		return r, err
	}

	meta.SetStatusCondition(&obsUI.Status.Conditions, metav1.Condition{Type: common.TypeAvailableObservabilityUI,
		Status: metav1.ConditionTrue, Reason: "Reconciling",
		Message: fmt.Sprintf("Resources for custom resource (%s) created successfully", obsUI.Name)})

	if err := r.Status().Update(ctx, obsUI); err != nil {
		log.Error(err, "Failed to update ObservailityUI status")
		return subreconciler.RequeueWithError(err)
	}

	return subreconciler.DoNotRequeue()
}

func (r *ObservabilityUIReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ObservabilityUI{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

type jsonPatch struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}
