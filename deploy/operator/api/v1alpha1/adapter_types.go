package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AdapterSpec defines the desired state of Adapter.
// Fields mirror the gateway's create-adapter payload.
type AdapterSpec struct {
	// Name is the human-readable name of the adapter.
	Name string `json:"name"`
	// Type identifies the adapter kind (e.g. "openai", "bedrock", "ollama").
	Type string `json:"type"`
	// Config holds adapter-specific key-value configuration (e.g. API keys, region).
	Config map[string]string `json:"config,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=adp

// Adapter is the Schema for the adapters API.
type Adapter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AdapterSpec    `json:"spec,omitempty"`
	Status ResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AdapterList contains a list of Adapter.
type AdapterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Adapter `json:"items"`
}
