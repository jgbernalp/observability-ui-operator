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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ObservabilityUIPluginService defines a service endpoint for a UI plugin
type ObservabilityUIPluginService struct {
	// Alias name of the service
	// +kubebuilder:validation:Required
	Alias string `json:"alias"`
	// Name is the name of the service
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// Namespace is the Kubernetes namespace where the service is located
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
	// Port is the port number of the service
	// +kubebuilder:validation:Required
	Port int32 `json:"port"`
}

// ObservabilityUIPluginSpec defines the desired state of ObservabilityUIPlugin
type ObservabilityUIPluginSpec struct {
	// DisplayName is the name displayed to identify the plugin
	// +kubebuilder:validation:Required
	DisplayName string `json:"displayName"`
	// Version of the plugin
	// +kubebuilder:validation:Required
	Version string `json:"version"`
	// Type is the type of the plugin, e.g., logs, metrics, etc.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=logs;distributed-tracing;dashboards;korrel8r
	Type string `json:"type"`
	// Services is an array of service endpoints for the plugin
	// +kubebuilder:validation:Optional
	Services []ObservabilityUIPluginService `json:"services"`
	// Config is an optional configuration for the plugin
	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Config map[string]string `json:"config,omitempty"`
}

// ObservabilityUIPluginStatus defines the observed state of ObservabilityUIPlugin
type ObservabilityUIPluginStatus struct {
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// ObservabilityUIPlugin is the Schema for the observabilityuiplugins API
type ObservabilityUIPlugin struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ObservabilityUIPluginSpec   `json:"spec,omitempty"`
	Status ObservabilityUIPluginStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ObservabilityUIPluginList contains a list of ObservabilityUIPlugin
type ObservabilityUIPluginList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObservabilityUIPlugin `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ObservabilityUIPlugin{}, &ObservabilityUIPluginList{})
}
