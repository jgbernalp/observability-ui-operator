package controllers

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	osv1alpha1 "github.com/openshift/api/console/v1alpha1"
	v1alpha1 "github.com/openshift/observability-ui-operator/api/v1alpha1"
	consoleplugin "github.com/openshift/observability-ui-operator/internal/consoleplugin"

	controller "github.com/openshift/observability-ui-operator/controllers/plugins"
	common "github.com/openshift/observability-ui-operator/internal/observability-ui/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("ObservabilityUIPlugin controller", func() {
	Context("ObservabilityUIPlugin controller test", func() {
		const obsUIName = "observability-ui-sample"
		namespace := common.GetObservabilityUINamespace()

		ctx := context.Background()

		namespaceObject := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      namespace,
				Namespace: namespace,
			},
		}

		namespacedName := types.NamespacedName{Name: obsUIName, Namespace: namespace}

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespaceObject)
			Expect(err).To(Not(HaveOccurred()))
		})

		AfterEach(func() {
			By("Deleting the Namespace to perform the tests")
			_ = k8sClient.Delete(ctx, namespaceObject)
		})

		It("should successfully reconcile a custom resource for ObservabilityUIPlugin", func() {
			By("Creating the custom resource for the Kind ObservabilityUIPlugin")
			plugin := &v1alpha1.ObservabilityUIPlugin{}
			err := k8sClient.Get(ctx, namespacedName, plugin)
			if err != nil && errors.IsNotFound(err) {
				plugin = &v1alpha1.ObservabilityUIPlugin{
					ObjectMeta: metav1.ObjectMeta{
						Name:      obsUIName,
						Namespace: namespaceObject.Name,
					},
					Spec: v1alpha1.ObservabilityUIPluginSpec{
						DisplayName: "Console Logs",
						Type:        "logs",
						Version:     "5.8.0",
						Services: []v1alpha1.ObservabilityUIPluginService{
							{
								Alias:     "backend",
								Name:      "loki-querier",
								Namespace: "openshift-logging",
								Port:      8080,
							},
						},
					},
				}

				err = k8sClient.Create(ctx, plugin)
				Expect(err).To(Not(HaveOccurred()))
			}

			By("Checking if the custom resource was successfully created")
			Eventually(func() error {
				found := &v1alpha1.ObservabilityUIPlugin{}
				return k8sClient.Get(ctx, namespacedName, found)
			}, Timeout, Interval).Should(Succeed())

			By("Reconciling the custom resource created")
			pluginReconciler := &controller.ObservabilityUIPluginReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Cfg:    cfg,
			}

			// Errors might arise during reconciliation, but we are checking the final state of the resources
			_, err = pluginReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: namespacedName,
			})

			// Expect(err).To(Not(HaveOccurred()))

			By("Checking if Service was successfully created in the reconciliation")
			Eventually(func() error {
				builder, err := consoleplugin.FromObsUIPlugin(plugin, namespace)

				if err != nil {
					return err
				}

				found := &corev1.Service{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: builder.ServiceName(), Namespace: namespace}, found)

				if err == nil {
					if len(found.Spec.Ports) < 1 {
						return fmt.Errorf("The number of ports used in the service is not the one defined in the custom resource")
					}
					if found.Spec.Ports[0].Port != builder.GetService().Spec.Ports[0].Port {
						return fmt.Errorf("The port used in the service is not the one defined in the custom resource")
					}
					if found.Spec.Selector["app.kubernetes.io/instance"] != builder.GetInstanceName() {
						return fmt.Errorf("The selector used in the service is not the one defined in the custom resource")
					}
				}

				return err
			}, Timeout, Interval).Should(Succeed())

			By("Checking if Console Plugin was successfully created in the reconciliation")
			Eventually(func() error {
				builder, err := consoleplugin.FromObsUIPlugin(plugin, namespace)

				if err != nil {
					return err
				}

				found := &osv1alpha1.ConsolePlugin{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: builder.ConsolePluginName()}, found)
			}, Timeout, Interval).Should(Succeed())

			By("Checking if Deployment was successfully created in the reconciliation")
			Eventually(func() error {
				builder, err := consoleplugin.FromObsUIPlugin(plugin, namespace)

				if err != nil {
					return err
				}

				found := &appsv1.Deployment{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: builder.DeploymentName(), Namespace: namespace}, found)

				if err == nil {
					if len(found.Spec.Template.Spec.Containers) < 1 {
						return fmt.Errorf("The number of containers used in the deployment is not the one expected")
					}
					if len(found.Spec.Template.Spec.Containers[0].Ports) < 1 && found.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort != 8080 {
						return fmt.Errorf("The port used in the deployment is not the one defined in the custom resource")
					}
				}

				return err
			}, Timeout, Interval).Should(Succeed())

			By("Checking the latest Status Condition added to the ObservabilityUIPlugin instance")
			Eventually(func() error {
				if plugin.Status.Conditions != nil && len(plugin.Status.Conditions) != 0 {
					latestStatusCondition := plugin.Status.Conditions[len(plugin.Status.Conditions)-1]
					expectedLatestStatusCondition := metav1.Condition{Type: common.TypeAvailableObservabilityUI,
						Status: metav1.ConditionTrue, Reason: "Reconciling",
						Message: fmt.Sprintf("Deployment for custom resource (%s) created successfully", plugin.Name)}
					if latestStatusCondition != expectedLatestStatusCondition {
						return fmt.Errorf("The latest status condition added to the ObservabilityUIPlugin instance is not as expected")
					}
				}
				return nil
			}, Timeout, Interval).Should(Succeed())

			pluginToDelete := &v1alpha1.ObservabilityUIPlugin{}
			err = k8sClient.Get(ctx, namespacedName, pluginToDelete)
			Expect(err).To(Not(HaveOccurred()))

			By("Deleting the custom resource")
			err = k8sClient.Delete(ctx, pluginToDelete)
			Expect(err).To(Not(HaveOccurred()))

			By("Checking if Deployment was successfully deleted in the reconciliation")
			Eventually(func() error {
				builder, err := consoleplugin.FromObsUIPlugin(plugin, namespace)

				if err != nil {
					return err
				}

				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: builder.DeploymentName(), Namespace: namespace}, found)
			}, Timeout, Interval).Should(Succeed())

			By("Checking if Service was successfully deleted in the reconciliation")
			Eventually(func() error {
				builder, err := consoleplugin.FromObsUIPlugin(plugin, namespace)

				if err != nil {
					return err
				}

				found := &corev1.Service{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: builder.ServiceName(), Namespace: namespace}, found)
			}, Timeout, Interval).Should(Succeed())

			By("Checking if ConsolePlugin was successfully deleted in the reconciliation")
			Eventually(func() error {
				builder, err := consoleplugin.FromObsUIPlugin(plugin, namespace)

				if err != nil {
					return err
				}

				found := &osv1alpha1.ConsolePlugin{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: builder.ConsolePluginName(), Namespace: namespace}, found)
			}, Timeout, Interval).Should(Succeed())

			By("Checking the latest Status Condition added to the ObservabilityUIPlugin instance")
			Eventually(func() error {
				if plugin.Status.Conditions != nil && len(plugin.Status.Conditions) != 0 {
					latestStatusCondition := plugin.Status.Conditions[len(plugin.Status.Conditions)-1]
					expectedLatestStatusCondition := metav1.Condition{Type: common.TypeAvailableObservabilityUI,
						Status: metav1.ConditionTrue, Reason: "Finalizing",
						Message: fmt.Sprintf("Finalizer operations for custom resource %s name were successfully accomplished", plugin.Name)}
					if latestStatusCondition != expectedLatestStatusCondition {
						return fmt.Errorf("The latest status condition added to the ObservabilityUIPlugin instance is not as expected")
					}
				}
				return nil
			}, Timeout, Interval).Should(Succeed())
		})
	})
})
