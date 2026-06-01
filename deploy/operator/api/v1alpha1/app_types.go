package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AppSpec defines the desired state of App.
// Fields mirror the gateway's create-app payload.
type AppSpec struct {
	// Name is the human-readable name of the application.
	Name string `json:"name"`
	// Description is a free-text description of the application's purpose.
	Description string `json:"description,omitempty"`
	// Targets is a list of Target resource names this app routes traffic to.
	Targets []string `json:"targets,omitempty"`
}

// ResourceStatus is the shared status sub-resource for all UBAG operator resources.
type ResourceStatus struct {
	// ObservedGeneration is the .metadata.generation the controller last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Ready indicates whether the resource has been successfully synced to the gateway.
	Ready bool `json:"ready"`
	// LastSyncedHash is the hash of the spec that was last successfully synced.
	LastSyncedHash string `json:"lastSyncedHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ubagapp

// App is the Schema for the apps API.
type App struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AppSpec        `json:"spec,omitempty"`
	Status ResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AppList contains a list of App.
type AppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []App `json:"items"`
}
