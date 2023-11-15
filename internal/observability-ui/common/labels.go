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

package common

import (
	"fmt"
)

func LabelsForObservabilitUI(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name,
		"app.kubernetes.io/part-of":    "observability-ui-operator",
		"app.kubernetes.io/created-by": "controller-manager",
		"app.kubernetes.io/instance":   "observability-ui",
		"app.kubernetes.io/managed-by": "observability-ui-operator",
	}
}

func ImageForObservabilityUIHub() (string, error) {
	return "quay.io/gbernal/observability-ui-hub:latest", nil
}

func GetConfigName(instanceName string) string {
	return fmt.Sprintf("%s-config", instanceName)
}

func GetObservabilityUINamespace() string {
	// TODO: This should be a parameter or come from installation context
	return "openshift-observability-ui"
}
