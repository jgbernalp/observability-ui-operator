package consoleplugin

import (
	"fmt"

	osv1 "github.com/openshift/api/console/v1"
	osv1alpha1 "github.com/openshift/api/console/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/openshift/observability-ui-operator/api/v1alpha1"
)

type ConsolePluginBuilder struct {
	namespace          string
	pluginName         string
	pluginVersion      string
	displayName        string
	port               int32
	services           []v1alpha1.ObservabilityUIPluginService
	replicas           *int32
	image              string
	serviceAccountName string
}

type PluginImageName struct {
	Image string
	Name  string
}

func getPluginImageFromType(pluginType string, version string) (*PluginImageName, error) {
	switch pluginType {
	case "logs":
		return &PluginImageName{
			Image: fmt.Sprintf("quay.io/gbernal/logging-view-plugin:%s", version),
			Name:  "logging-view-plugin",
		}, nil
	case "dashboards":
		return &PluginImageName{
			Image: fmt.Sprintf("quay.io/gbernal/console-dashboards-plugin:%s", version),
			Name:  "console-dashboards-plugin",
		}, nil
	}

	return nil, fmt.Errorf("invalid plugin type: %s", pluginType)
}

func FromObsUIPlugin(obsUIPlugin *v1alpha1.ObservabilityUIPlugin, namespace string) (*ConsolePluginBuilder, error) {
	pluginInfo, err := getPluginImageFromType(obsUIPlugin.Spec.Type, obsUIPlugin.Spec.Version)

	if err != nil {
		return nil, err
	}

	return &ConsolePluginBuilder{
		namespace:     namespace,
		pluginName:    pluginInfo.Name,
		pluginVersion: obsUIPlugin.Spec.Version,
		displayName:   obsUIPlugin.Spec.DisplayName,
		port:          9443,
		image:         pluginInfo.Image,
		services:      obsUIPlugin.Spec.Services,
	}, nil
}

func FromObsUI(obsUI *v1alpha1.ObservabilityUI, namespace string) (*ConsolePluginBuilder, error) {
	version := "dev"
	pluginName := "observability-ui-hub"
	image := fmt.Sprintf("quay.io/openshift-observability-ui/observability-ui-hub:%s", version)

	return &ConsolePluginBuilder{
		namespace:     namespace,
		pluginName:    pluginName,
		pluginVersion: version,
		displayName:   "Observability UI Hub",
		port:          obsUI.Spec.Deployment.Port,
		image:         image,
		services: []v1alpha1.ObservabilityUIPluginService{{
			Alias:     "backend",
			Namespace: namespace,
			Name:      pluginName,
			Port:      obsUI.Spec.Deployment.Port,
		}},
		serviceAccountName: pluginName,
	}, nil
}

func (b *ConsolePluginBuilder) ServiceName() string {
	return b.pluginName
}

func (b *ConsolePluginBuilder) DeploymentName() string {
	return b.pluginName
}

func (b *ConsolePluginBuilder) ConsolePluginName() string {
	return b.pluginName
}

func (b *ConsolePluginBuilder) GetConsolePluginV1Alpha1() *osv1alpha1.ConsolePlugin {
	var proxies []osv1alpha1.ConsolePluginProxy

	for _, service := range b.services {
		proxies = append(proxies, osv1alpha1.ConsolePluginProxy{
			Type:      osv1alpha1.ProxyTypeService,
			Alias:     service.Alias,
			Authorize: true,
			Service: osv1alpha1.ConsolePluginProxyServiceConfig{
				Name:      service.Name,
				Namespace: service.Namespace,
				Port:      service.Port,
			},
		})
	}

	return &osv1alpha1.ConsolePlugin{
		ObjectMeta: metav1.ObjectMeta{
			Name: b.pluginName,
		},
		Spec: osv1alpha1.ConsolePluginSpec{
			DisplayName: b.displayName,
			Service: osv1alpha1.ConsolePluginService{
				Name:      b.pluginName,
				Namespace: b.namespace,
				Port:      b.port,
				BasePath:  "/",
			},
			Proxy: proxies,
		},
	}
}

func (b *ConsolePluginBuilder) GetSecretName() string {
	return fmt.Sprintf("%s-secret", b.pluginName)
}

func (b *ConsolePluginBuilder) GetInstanceName() string {
	return fmt.Sprintf("%s-%s", b.pluginName, b.pluginVersion)
}

func (b *ConsolePluginBuilder) GetService() *corev1.Service {
	labels := map[string]string{
		"app.kubernetes.io/name":       b.pluginName,
		"app.kubernetes.io/instance":   b.GetInstanceName(),
		"app.kubernetes.io/version":    b.pluginVersion,
		"app.kubernetes.io/part-of":    b.pluginName,
		"app.kubernetes.io/created-by": "ui-plugin-controller-manager",
		"app.kubernetes.io/managed-by": "observability-ui-operator",
		"app":                          b.pluginName,
	}

	// Create a serving cetfiicate secret for the service
	annotations := map[string]string{
		"service.alpha.openshift.io/serving-cert-secret-name": b.GetSecretName(),
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        b.pluginName,
			Namespace:   b.namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       b.port,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.FromInt32(b.port),
			}},
			Selector: labels,
		},
	}
}

func (b *ConsolePluginBuilder) GetConsolePluginV1() *osv1.ConsolePlugin {
	var proxies []osv1.ConsolePluginProxy

	for _, service := range b.services {
		proxies = append(proxies, osv1.ConsolePluginProxy{
			Alias:         service.Alias,
			Authorization: "UserToken",
			Endpoint: osv1.ConsolePluginProxyEndpoint{
				Type: osv1.ProxyTypeService,
				Service: &osv1.ConsolePluginProxyServiceConfig{
					Name:      service.Name,
					Namespace: service.Namespace,
					Port:      service.Port,
				},
			},
		})
	}

	if len(b.services) == 0 {
		proxies = nil
	}

	return &osv1.ConsolePlugin{
		ObjectMeta: metav1.ObjectMeta{
			Name: b.pluginName,
		},
		Spec: osv1.ConsolePluginSpec{
			DisplayName: b.displayName,
			Backend: osv1.ConsolePluginBackend{
				Type: "Service",
				Service: &osv1.ConsolePluginService{
					Name:      b.pluginName,
					Namespace: b.namespace,
					Port:      b.port,
					BasePath:  "/",
				},
			},
			Proxy: proxies,
		},
	}
}

func (b *ConsolePluginBuilder) GetDeployment() *appsv1.Deployment {
	// TODO add created by label
	labels := map[string]string{
		"app.kubernetes.io/name":       b.DeploymentName(),
		"app.kubernetes.io/instance":   b.GetInstanceName(),
		"app.kubernetes.io/version":    b.pluginVersion,
		"app.kubernetes.io/part-of":    b.pluginName,
		"app.kubernetes.io/created-by": "ui-plugin-controller-manager",
		"app.kubernetes.io/managed-by": "observability-ui-operator",
		"app":                          b.pluginName,
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.DeploymentName(),
			Namespace: b.namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: b.replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					// TODO(user): Uncomment the following code to configure the nodeAffinity expression
					// according to the platforms which are supported by your solution. It is considered
					// best practice to support multiple architectures. build your manager image using the
					// makefile target docker-buildx. Also, you can use docker manifest inspect <image>
					// to check what are the platforms supported.
					// More info: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity
					//Affinity: &corev1.Affinity{
					//	NodeAffinity: &corev1.NodeAffinity{
					//		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					//			NodeSelectorTerms: []corev1.NodeSelectorTerm{
					//				{
					//					MatchExpressions: []corev1.NodeSelectorRequirement{
					//						{
					//							Key:      "kubernetes.io/arch",
					//							Operator: "In",
					//							Values:   []string{"amd64", "arm64", "ppc64le", "s390x"},
					//						},
					//						{
					//							Key:      "kubernetes.io/os",
					//							Operator: "In",
					//							Values:   []string{"linux"},
					//						},
					//					},
					//				},
					//			},
					//		},
					//	},
					//},
					Containers: []corev1.Container{{
						Image:           b.image,
						Name:            b.pluginName,
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             &[]bool{true}[0],
							AllowPrivilegeEscalation: &[]bool{false}[0],
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{
									"ALL",
								},
							},
						},
						Ports: []corev1.ContainerPort{{
							ContainerPort: b.port,
							Name:          "web",
						}},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "serving-cert",
								ReadOnly:  true,
								MountPath: "/var/serving-cert",
							},
						},
						Args: []string{
							fmt.Sprintf("-port=%d", b.port),
							"-cert=/var/serving-cert/tls.crt",
							"-key=/var/serving-cert/tls.key",
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "serving-cert",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName:  b.GetSecretName(),
									DefaultMode: &[]int32{420}[0],
								},
							},
						},
						// 	{
						// 		Name: "config",
						// 		VolumeSource: corev1.VolumeSource{
						// 			ConfigMap: &corev1.ConfigMapVolumeSource{
						// 				LocalObjectReference: corev1.LocalObjectReference{
						// 					Name: configName,
						// 				},
						// 				DefaultMode: &[]int32{420}[0],
						// 			},
						// 		},
						// 	},
					},
					RestartPolicy:      "Always",
					DNSPolicy:          "ClusterFirst",
					ServiceAccountName: b.serviceAccountName,
				},
			},
		},
	}
}
