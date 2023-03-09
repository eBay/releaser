package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// +genclient
// +kubebuilder:resource:shortName="rcal"
// +kubebuilder:printcolumn:name="Template",type="string",JSONPath=".spec.templateRef.name"
// +kubebuilder:printcolumn:name="Paused",type="boolean",JSONPath=".spec.paused"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ReleaseCalendar describes a Release arragement from calendar view.
type ReleaseCalendar struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReleaseCalendarSpec `json:"spec,omitempty"`
}

// ReleaseCalendarSpec describes how and when this calendar should be executed.
type ReleaseCalendarSpec struct {
	// Paused when set to true, the execution of this Event should stop. All
	// the running PipelineRuns will still run till completion.
	Paused bool `json:"paused,omitempty"`
	// TemplateRef specifies the available PipelineRun templates. Usually it
	// is a ConfigMap, and each key in this ConfigMap can represent one stage
	// of each event.
	TemplateRef TemplateRef `json:"templateRef"`
	// Params is a list of Name-Value pairs that will be passed as parameters
	// to all the PipelineRuns.
	Params []Param `json:"params,omitempty"`
	// Pipelines describes a sequential Pipelines that should be executed for
	// each ReleaseEvent.
	Pipelines []ReleaseCalendarPipeline `json:"pipelines"`
	// SkipDays specifies the days when releaseevents shouldn't be scheduled.
	SkipDays []string `json:"skipDays,omitempty"`
	// Weekdays specifies the weekdays when releaseevents can be scheduled.
	Weekdays []ReleaseCalendarDay `json:"weekdays,omitempty"`
	// Hours specifies the hours when release events can execute. This usually
	// aligns with working hours, for e.g, 9:00AM-05:00PM.
	Hours []ReleaseCalendarHour `json:"hours,omitempty"`
	// TimeZone specifies the applicable timezone. For, e.g -8 means PST.
	TimeZone intstr.IntOrString `json:"timeZone"`
	// GroupPolicy defines the policy of grouping events' execution
	GroupPolicy ReleaseCalendarGroupPolicy `json:"groupPolicy,omitempty"`
	// ConcurrencePolicy specifies what happens to an old Pending event
	// when new events with the same name get created.
	// If this is not specified, the new event will wait for the old event to be
	// done (Completed, Failed or Cancelled)
	ConcurrentPolicy ReleaseCalendarConcurrentPolicy `json:"concurrentPolicy,omitempty"`
}

type ReleaseCalendarDay struct {
	// From specifies the start day of week. For e.g, 0 means Sunday.
	// +kubebuilder:validation:Maximum=6
	// +kubebuilder:validation:Minimum=0
	From int64 `json:"from"`
	// To specifies the end day of week. For e.g, 4 means Thursday.
	// +kubebuilder:validation:Maximum=6
	// +kubebuilder:validation:Minimum=0
	To int64 `json:"to"`
}

// ReleaseCalendarHour specifies a range of time that this calendar considers as
// active time periods.
type ReleaseCalendarHour struct {
	// From specifies the starting hour of the day. For e.g, 10 means 10AM.
	// If unset or set to 0, it means the begining of the day.
	// +kubebuilder:validation:Maximum=47
	// +kubebuilder:validation:Minimum=0
	From int64 `json:"from"`
	// To specifies the end hour of the day. For e.g, 17 means 5PM.
	// If To is greater or equal to 24, the hour falls into the second day.
	// +kubebuilder:validation:Maximum=47
	// +kubebuilder:validation:Minimum=0
	To int64 `json:"to"`
	// Params specifies the parameters that should be applied to all Events
	// in this time range.
	Params []Param `json:"params,omitempty"`
}

// TemplateRef is about where we can read all the PipelineRun templates.
type TemplateRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// Param is a key value pair which can consumed in PipelineRuns for all Release
// Events.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ReleaseCalendarGroupPolicy string

const (
	// GroupPolicyAll means events can be in the same execution group as long as they have the same group name
	GroupPolicyAll ReleaseCalendarGroupPolicy = "All"
	// GroupPolicyDaily means events can be in the same execution group if they start in the same day (with timezone)
	GroupPolicyDaily ReleaseCalendarGroupPolicy = "Daily"
)

type ReleaseCalendarConcurrentPolicy string

const (
	// ConcurrentPolicyDelete will delete the previous Pending event
	ConcurrentPolicyDelete ReleaseCalendarConcurrentPolicy = "Delete"
	// ConcurrentPolicyWait will wait for the previous Pending event
	ConcurrentPolicyWait ReleaseCalendarConcurrentPolicy = "Wait"
)

// ReleaseCalendarPipeline specifies how and when a PipelineRun should be executed.
type ReleaseCalendarPipeline struct {
	// Name is a key to the specified TemplateRef.
	Name string `json:"name"`
	// TimeOffset specifies the relative time from the scheduled event Time.
	// There are two formats supported:
	// 1. "-6h": This Pipeline should be executed 6 hours ahead.
	// 2. "-1d,10:00": This Pipeline should be exeucted at 10:00am from
	//    the previous day.
	TimeOffset string `json:"timeOffset,omitempty"`
	// RunPolicy specifies the policy for this Pipeline. It might be:
	// 1. LeaderOnly: This Pipeline is only executed on leader events.
	// 2. Always: This Pipeline should be executed for all events.
	RunPolicy ReleaseCalendarRunPolicy `json:"runPolicy,omitempty"`
	// AfterPolicy specifies whether the execution of this Pipeline should
	// honor event After settings. If this set to Always and A has B as after,
	// then this Pipeline for event A can only run when event B finishes the
	// same Pipeline.
	AfterPolicy ReleaseCalendarAfterPolicy `json:"afterPolicy,omitempty"`
	// Rollback is a key to the specified TemplateRef. The template is used
	// to create PipelineRun that is meant for rollback.
	Rollback string `json:"rollback,omitempty"`
	// RollbackPolicy specifies when the rollback PipelineRun should run.
	// This could be Auto so that rollback will happen automatically on
	// failure, or if set to None, then this rollback Pipeline can only
	// be triggered manually.
	RollbackPolicy ReleaseCalendarRollbackPolicy `json:"rollbackPolicy,omitempty"`

	// Other ideas:
	// PausePolicy: Is it helpful to automatically update event.paused = true
	//              when this Pipeline completes? This probably means that
	//              we can inject human check in between.
}

// ReleaseCalendarRunPolicy specifies which events should run this PipelineRun.
type ReleaseCalendarRunPolicy string

const (
	// RunPolicyAlways means all the events should run.
	RunPolicyAlways ReleaseCalendarRunPolicy = "Always"
	// RunPolicyLeaderOnly means only the leader should run.
	RunPolicyLeaderOnly ReleaseCalendarRunPolicy = "LeaderOnly"
)

// ReleaseCalendarAfterPolicy specifies how does the RunAfter policy on Release
// events affect the PipelineRun execution.
type ReleaseCalendarAfterPolicy string

const (
	// AfterPolicyIgnore means the time to run doesn't look for completeness
	// of its precedent events.
	AfterPolicyIgnore ReleaseCalendarAfterPolicy = "Ignore"
	// AfterPolicyAlways means the time to run relies on the completion time
	// of its precedent events.
	AfterPolicyAlways ReleaseCalendarAfterPolicy = "Always"
)

// ReleaseCalendarRollbackPolicy specifies how should Rollback happen.
type ReleaseCalendarRollbackPolicy string

const (
	// RollbackPolicyAuto means the rollback should happen on failure without
	// human interaction.
	RollbackPolicyAuto ReleaseCalendarRollbackPolicy = "Auto"
	// RollbackPolicyNone means the rollback can only be triggered manually.
	RollbackPolicyNone ReleaseCalendarRollbackPolicy = "None"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ReleaseCalendarList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of calendars.
	Items []ReleaseCalendar `json:"items"`
}

// +genclient
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName="revent"
// +kubebuilder:printcolumn:name="Calendar",type="string",JSONPath=".spec.calendar"
// +kubebuilder:printcolumn:name="Time",type="string",format="date-time",JSONPath=".status.time"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ReleaseEvent describes an event with specific parameters. This event runs
// through all the Pipelines as specified in the corresponding ReleaseCalendar.
type ReleaseEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReleaseEventSpec   `json:"spec,omitempty"`
	Status            ReleaseEventStatus `json:"status,omitempty"`
}

// ReleaseEventSpec describes the ReleaseEvent details.
type ReleaseEventSpec struct {
	// Calendar is the name of ReleaseCalendar this event belongs to.
	Calendar string `json:"calendar"`
	// Paused when set to true, the execution of this Event should stop. All
	// the running PipelineRuns will still run till completion.
	Paused bool `json:"paused,omitempty"`
	// Time is the execution time scheduled for this event.
	Time *metav1.Time `json:"time,omitempty"`
	// Params is a list of Name-Value pairs that should be passed to all of
	// PipelineRuns.
	Params []Param `json:"params,omitempty"`
	// Note is additional information that people can put on.
	Note string `json:"note,omitempty"`
	// Group is a name to identify the group this event belongs to. If there
	// are multiple events belong to the same group, a leader will be elected.
	Group string `json:"group,omitempty"`
	// RunAfter declares a list of precedent events this event should run
	// after. This supersedes the After field.
	RunAfter []ReleaseEventRunAfter `json:"runAfter,omitempty"`
}

// ReleaseEventRunAfter specifies how the current event should run after another
// one.
type ReleaseEventRunAfter struct {
	// Name is name of the event it should wait for.
	Name string `json:"name"`
	// Pipeline is the name of pipeline it should wait for.
	Pipeline string `json:"pipeline,omitempty"`
	// TimeOffset specifies when this event should start. There are two
	// formats supported:
	// 1. "6h": This means 6 hours after.
	// 2. "24h,10:00": This means 10:00 at the next day.
	// This must be a future time (A positive time duration).
	// Note: The offset is relative to the status.time of another release event.
	TimeOffset string `json:"timeOffset,omitempty"`
}

// ReleaseEventStatus describes the current status of this ReleaseEvent.
type ReleaseEventStatus struct {
	// Phase describes the current event phase.
	Phase ReleaseEventPhase `json:"phase,omitempty"`
	// Time is the estimated or actual (if the event is already Running)
	// execution time for this event.
	Time *metav1.Time `json:"time,omitempty"`
	// Leader is the name of release event which acts as the group leader.
	Leader string `json:"leader,omitempty"`
	// PipelineRuns describes all the executed PipelineRun status.
	PipelineRuns []ReleaseEventPipelineRun `json:"pipelineRuns,omitempty"`
}

// ReleaseEventPhase describes the current phase of this ReleaseEvent.
type ReleaseEventPhase string

const (
	// EventStatusPending means no PipelineRun has been created yet.
	EventStatusPending ReleaseEventPhase = "Pending"
	// EventStatusRunning means there is at least one PipelineRun Running.
	EventStatusRunning ReleaseEventPhase = "Running"
	// EventStatusCompleted means all the PipelineRuns have Completed.
	EventStatusCompleted ReleaseEventPhase = "Completed"
	// EventStatusSkipped means there is nothing left to run for this event
	// and some steps are skipped. This should be treated same as Completed.
	EventStatusSkipped ReleaseEventPhase = "Skipped"
	// EventStatusCancelled means the last PipelineRun is Cancelled.
	EventStatusCancelled ReleaseEventPhase = "Cancelled"
	// EventStatusFailed means the last PipelineRun is Failed.
	EventStatusFailed ReleaseEventPhase = "Failed"
)

// ReleaseEventPipelineRun describes a specific PipelineRun status in current
// ReleaseEvent.
type ReleaseEventPipelineRun struct {
	// Name is the name of PipelineRun that are created.
	Name string `json:"name"`
	// Cluster denotes where the PipelineRun is created.
	Cluster string `json:"cluster,omitempty"`
	// Pipeline is the name of Pipeline stage (as specified from ReleaseCalendar).
	Pipeline string `json:"pipeline"`
	// StartTime describes the start time of this PipelineRun.
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// CompletionTime describes the completion time of this PipelineRun.
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// Status describes the PipelineRun status.
	Status ReleaseEventPipelineRunStatus `json:"status"`
	// Results describes the PipelineRun results.
	Results map[string]string `json:"results,omitempty"`
}

// ReleaseEventPipelineRunStatus describes the PipelineRun status.
type ReleaseEventPipelineRunStatus string

const (
	// PipelineRunStatusPending means this PipelineRun is waiting to be executed.
	PipelineRunStatusPending ReleaseEventPipelineRunStatus = "Pending"
	// PipelineRunStatusRunning means this PipelineRun is being executed.
	PipelineRunStatusRunning ReleaseEventPipelineRunStatus = "Running"
	// PipelineRunStatusCompleted means this PipelineRun is completed.
	PipelineRunStatusCompleted ReleaseEventPipelineRunStatus = "Completed"
	// PipelineRunStatusCancelled means this PipelineRun is cancalled.
	PipelineRunStatusCancelled ReleaseEventPipelineRunStatus = "Cancelled"
	// PipelineRunStatusFailed means this PipelineRun is failed.
	PipelineRunStatusFailed ReleaseEventPipelineRunStatus = "Failed"
	// PipelineRunStatusRollback means this PipelineRun is failed and Rollback is triggered.
	PipelineRunStatusRollback ReleaseEventPipelineRunStatus = "Rollback"
	// PipelineRunStatusUnknown means the status is currently unknown.
	PipelineRunStatusUnknown ReleaseEventPipelineRunStatus = "Unknown"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ReleaseEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of releaseevents.
	Items []ReleaseEvent `json:"items"`
}
