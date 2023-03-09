package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName="re"
// +kubebuilder:printcolumn:name="Repository",type="string",JSONPath=".spec.repository"
// +kubebuilder:printcolumn:name="Commit",type="string",JSONPath=".status.commit"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Release represents the desired state for a certain component by referencing
// its git repository at a specific commit.
type Release struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReleaseSpec   `json:"spec,omitempty"`
	Status ReleaseStatus `json:"status,omitempty"`
}

// ReleaseSpec defines the components which consist of a Release. Each release
// has a Runner deployment which is created from this ReleaseSpec.
type ReleaseSpec struct {
	// Repository is the name of git repository, it is normally like <org>/<name>.
	Repository string `json:"repository"`
	// Branch is the branch which we should pull from. Default to master.
	Branch string `json:"branch,omitempty"`
	// Revision specifies the revision which we should deploy from. Default
	// to HEAD.
	Revision string `json:"revision,omitempty"`
	// Includes specifies the directory or files that should be checked out.
	// +Optional
	Includes []string `json:"includes,omitempty"`
	// Excludes specifies the directory or files that should be skipped.
	// +Optional
	Excludes []string `json:"excludes,omitempty"`
	// SecretRef is a reference to secret where access token and gpg key are
	// defined. The secret could specify keys `token` and optionally `gpg.key`.
	// If token key is specified, then the repository will be pulled with it.
	// If gpg.key is specified, and repository is encrypted, the key will be
	// used to run decryption.
	// If left unspecified, the default tokens and gpg.key will be used.
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
	// ServiceAccountName specifies the identity used during the lifecycle of
	// this release. The identity may be used to run deployer and approver.
	// The token of this service account will be translated into a secret.
	// If unspecified, default service account is used.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// Deployer specifies how to deploy this release. It may support multiple
	// deployment options like kubectl or helm. Only one option can be set.
	Deployer ReleaseDeployer `json:"deployer"`

	// Disabled if set to true indicates that this release shouldn't be worked
	// on. This can be used in emergency case when people want to fix issues
	// manually.
	Disabled bool `json:"disabled,omitempty"`

	// RevisionHistoryLimit is the maximum number of revisions that will
	// be maintained in the Release's revision history. The revision history
	// consists of all revisions not represented by a currently applied
	// Release version. The default value is 10.
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`
}

// ReleaseDeployer describes the varies of deployer which can take the release
// and realize it.
type ReleaseDeployer struct {
	// DryRun if set to true indicates that this release isn't expected to
	// make real changes. The dryRun semantic is deployer specific. If the
	// deployer doesn't support dry run mode, it should report failure by
	// setting condition in status. Besides, checker and approver routines
	// won't be triggered, either.
	DryRun bool `json:"dryRun,omitempty"`
	// Reconcile is a flag to tell releaser whether to reconcile this release
	// actively. If this is not set or is false, the deployer won't try to
	// deploy again when the commit id is the same and its phase has entered
	// Succeeded or Failed. When set to be true, even when the commit ID is
	// the same and its phase is in Succeeded and Failed, the deployer will
	// still try to see whether there is a difference between source and live
	// specs, and when there is difference, it triggers the deployment flow
	// once again.
	// Be noted, when the release is not in Succeeded/Failed phase, we may
	// still retry the deployment no matter whether this is true or not.
	Reconcile bool `json:"reconcile,omitempty"`

	// Name is the plugin name, for example: kubectl:v1.15.6. The name also
	// indicates the image name when configuring the deployer container. The
	// mapping is done like below:
	//   kubectl => hub.github.com/ebay/releaser/kubectl:latest
	//   helm@v0.4.0 => hub.github.com/ebay/releaser/helm:v0.4.0
	//   hub.github.com/ebay/releaser/tess:v0.4.4 => hub.github.com/ebay/releaser/tess:v0.4.4
	Name string `json:"name,omitempty"`
	// Configuration is path to the configuration file. The format is defined
	// by each of the individual plugins, and it is recommended to version
	// its definition to improve compatibility.
	Configuration string `json:"configuration,omitempty"`
	// Parameters are unstructured inline configuration for deployer. The
	// usage of these values is deployer dependent.
	Parameters map[string]string `json:"parameters,omitempty"`
	// Timeout specifies how long it can take to run this deployment. The
	// default value is 20 minutes.
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// ReleaseStatus captures the status.
type ReleaseStatus struct {
	// The most recent generation observed by the release controller. If the
	// value is less than metadata.generation, the value of phase can not be
	// trusted. In another word, if the observedGeneration equals to metadata
	// generation, then phase must be correct.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase describes the current release phase.
	Phase ReleasePhase `json:"phase,omitempty"`
	// Commit is the current commit id which is already applied. It is empty
	// if there is nothing applied yet.
	Commit string `json:"commit,omitempty"`
	// ParametersHash is the sha256 value of applied parameters. This value
	// determines whether the parameters are changed.
	ParametersHash string `json:"parametersHash,omitempty"`
	// Version declares the logical release version information. The applier
	// looks for a VERSION file from the repository root and update it here.
	// Otherwise, this field is left as empty. This information is consumed
	// when there is dependency check required.
	Version string `json:"version,omitempty"`
	// DeployerStatus tells the information about deployer status.
	// TODO: rename it since a release now consists of deployer and approver
	// together.
	DeployerStatus *DeployerStatus `json:"deployerStatus,omitempty"`
	// Conditions tracks the varies of status of conditions.
	Conditions []ReleaseCondition `json:"conditions,omitempty"`
}

// ReleasePhase defines the phase of release.
type ReleasePhase string

const (
	// ReleasePending means the the release is not executed yet. This could
	// mean: 1) the setup of deployment and secret is not completed yet. 2)
	// the deployment is not running yet.
	ReleasePending ReleasePhase = "Pending"
	// ReleaseSpawning means the deployment is created/updated but not completed
	// yet. This could mean:
	// 1. Newly created, but pod is not coming up.
	// 2. Updated and new pod is not coming up yet.
	// 3. Updated and new pod is coming up, but old pod is not deleted.
	ReleaseSpawning ReleasePhase = "Spawning"
	// ReleaseRunning means the release is being executed. This indicates:
	// 1. The pod spawned is now up and running.
	// The status.commit and status.version can only be trusted when phase is
	// Running or post Running.
	ReleaseRunning ReleasePhase = "Running"
	// ReleaseSucceeded means the release is fully realized(deployed) and:
	// 1. checks are all passed if there are any.
	// 2. approvals are all done if there are any.
	ReleaseSucceeded ReleasePhase = "Succeeded"
	// ReleaseFailed means the release fails to proceed, and cannot be retried
	// unless changing the spec.
	ReleaseFailed ReleasePhase = "Failed"
	// ReleaseDisabled means the release has been disabled.
	ReleaseDisabled ReleasePhase = "Disabled"
)

// ReleaseConditionType defines the condition of Runner.
type ReleaseConditionType string

const (
	// DeployerReady means that the deployer is now up and running.
	DeployerReady ReleaseConditionType = "DeployerReady"
	// ReleaseDeployed means that there is a successful deployment.
	ReleaseDeployed ReleaseConditionType = "ReleaseDeployed"
	// ReleaseChecked means all the specs are reaching to their desired state
	ReleaseChecked ReleaseConditionType = "ReleaseChecked"

	// ReleaseInit tells the condition of Init stage.
	ReleaseInit ReleaseConditionType = "ReleaseInit"
	// ReleaseDiff tells the condition of Diff stage.
	ReleaseDiff ReleaseConditionType = "ReleaseDiff"
	// ReleaseRun tells the condition of Run stage.
	ReleaseRun ReleaseConditionType = "ReleaseRun"
	// ReleaseTest tells the condition of Test stage.
	ReleaseTest ReleaseConditionType = "ReleaseTest"
)

// ConditionStatus is True/False/Unknown.
type ConditionStatus string

// These are the possible values for ConditionStatus.
const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// ReleaseCondition represents runner's condition.
type ReleaseCondition struct {
	// Type is the type of the condition.
	Type ReleaseConditionType `json:"type"`
	// Status is the status of the condition.
	// Can be True, False, Unknown.
	Status ConditionStatus `json:"status"`
	// Last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// Unique, one-word, CamelCase reason for the condition's last transition.
	Reason string `json:"reason,omitempty"`
	// Human-readable message indicating details about last transition.
	Message string `json:"message,omitempty"`
}

// DeployerPhase describes the current phase of deployer.
type DeployerPhase string

const (
	// DeployerPhasePending means the deployment has no pod or the status is
	// out of date.
	DeployerPhasePending DeployerPhase = "Pending"
	// DeployerPhaseCreating is the same as Progressing but
	DeployerPhaseCreating DeployerPhase = "Creating"
	// DeployerPhaseProgressing means the deployment is being scaled up or down.
	DeployerPhaseProgressing DeployerPhase = "Progressing"
	// DeployerPhaseRunning means the deployment is running in desired state.
	DeployerPhaseRunning DeployerPhase = "Running"
)

// DeployerStatus tracks which deployment is responsible for this Release.
type DeployerStatus struct {
	Cluster        string        `json:"cluster,omitempty"`
	Namespace      string        `json:"namespace,omitempty"`
	DeploymentName string        `json:"deploymentName,omitempty"`
	SecretName     string        `json:"secretName,omitempty"`
	Phase          DeployerPhase `json:"phase,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ReleaseList is a list of Releases.
type ReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of releases.
	Items []Release `json:"items"`
}
