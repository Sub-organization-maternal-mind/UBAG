package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TargetSpec defines the desired state of Target.
// Fields mirror the gateway's create-target payload.
type TargetSpec struct {
	// Name is the human-readable name of the target.
	Name string `json:"name"`
	// URL is the endpoint of the backing LLM or service.
	URL string `json:"url"`
	// Tags are optional labels for routing or filtering.
	Tags []string `json:"tags,omitempty"`
	// Model is the model identifier (e.g. "gpt-4", "llama-3").
	Model string `json:"model,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tgt

// Target is the Schema for the targets API.
type Target struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TargetSpec     `json:"spec,omitempty"`
	Status ResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TargetList contains a list of Target.
type TargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Target `json:"items"`
}
