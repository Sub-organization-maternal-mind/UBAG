package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TemplateSpec defines the desired state of Template.
// Fields mirror the gateway's create-template payload.
type TemplateSpec struct {
	// Name is the human-readable name of the prompt template.
	Name string `json:"name"`
	// Content is the raw template body (may include {{variable}} placeholders).
	Content string `json:"content"`
	// Variables lists the placeholder variable names present in Content.
	Variables []string `json:"variables,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tmpl

// Template is the Schema for the templates API.
type Template struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TemplateSpec   `json:"spec,omitempty"`
	Status ResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TemplateList contains a list of Template.
type TemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Template `json:"items"`
}
