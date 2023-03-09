package calendar

import (
	"bytes"
	"context"
	"fmt"
	ttemplate "text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
)

func (c *EventController) createPipelineRun(event *fleetv1alpha2.ReleaseEvent, pipeline, cluster, name string, template string, params map[string]interface{}) error {
	tpl, err := ttemplate.New("").Funcs(sprig.TxtFuncMap()).Parse(template)
	if err != nil {
		return fmt.Errorf("failed to parse %s as template: %s", template, err)
	}
	var data bytes.Buffer
	if err := tpl.Execute(&data, params); err != nil {
		return fmt.Errorf("failed to execute template %s: %s", template, err)
	}

	obj, gvk, err := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme).Decode(data.Bytes(), nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode template: %s", err)
	}
	if gvk == nil {
		return fmt.Errorf("unknown gvk found in template: <nil>")
	}
	if gvk.Group != "tekton.dev" || gvk.Version != "v1beta1" || gvk.Kind != "PipelineRun" {
		return fmt.Errorf("unknown gvk found in template: %s", gvk.String())
	}

	pipelineRun, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unknown object in template: %#v", obj)
	}

	// We need to set Namespace and Name as we wish, and two more labels need to be added.
	pipelineRun.SetNamespace(event.Namespace)
	pipelineRun.SetName(name)
	labels := pipelineRun.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[LabelEventNamespace] = event.Namespace
	labels[LabelEventName] = event.Name
	labels[LabelEventPipeline] = pipeline
	pipelineRun.SetLabels(labels)

	_, err = c.tektonclients[cluster].Resource(schema.GroupVersionResource{
		Group:    "tekton.dev",
		Version:  "v1beta1",
		Resource: "pipelineruns",
	}).Namespace(event.Namespace).Create(context.TODO(), pipelineRun, metav1.CreateOptions{})

	return err
}

func (c *EventController) cancelPipelineRun(namespace, cluster, name string) error {
	obj, err := c.pipelineRunListers[cluster].ByNamespace(namespace).Get(name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get pipelinerun %s/%s: %s", namespace, name, err)
		}
		// when this pipelinerun is not found, there is nothing to cancel
		return nil
	}
	pipelineRun, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object in pipelineRunLister is not unstructured")
	}

	status, _, err := unstructured.NestedString(pipelineRun.Object, "spec", "status")
	if err != nil {
		return fmt.Errorf("faield to look at spec.status of %s/%s: %s", namespace, name, err)
	}
	if status == "Cancelled" || status == "PipelineRunCancelled" || status == "CancelledRunFinally" || status == "StoppedRunFinally" {
		return nil
	}

	err = unstructured.SetNestedField(pipelineRun.Object, "Cancelled", "spec", "status")
	if err != nil {
		return fmt.Errorf("failed to set spec.status to Cancelled for %s/%s: %s", namespace, name, err)
	}

	_, err = c.tektonclients[cluster].Resource(schema.GroupVersionResource{
		Group:    "tekton.dev",
		Version:  "v1beta1",
		Resource: "pipelineruns",
	}).Namespace(namespace).Update(context.TODO(), pipelineRun, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update spec.status to Cancelled for %s/%s: %s", namespace, name, err)
	}

	return nil
}

func (c *EventController) rollbackEventPipeline(event *fleetv1alpha2.ReleaseEvent, cluster, pipeline string, templates map[string]string, params map[string]interface{}) error {
	ok, index := getPipelineRunStatusOrInit(event, pipeline, cluster)
	if !ok {
		return nil
	}
	pipelineRunStatus := event.Status.PipelineRuns[index]
	if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusCompleted ||
		pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusFailed {
		return nil
	}

	pipelineRun, err := c.pipelineRunListers[cluster].ByNamespace(event.Namespace).Get(pipelineRunStatus.Name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get pipelinerun %s: %s", pipelineRunStatus.Name, err)
		}
		// This event is in Cancelled phase, we don't need to create a new PipelineRun
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusCancelled {
			return nil
		}
		// create pipelinerun
		template, ok := lookupYAML(templates, pipeline)
		if !ok {
			return fmt.Errorf("pipeline %s doesn't exist in template", pipeline)
		}
		err := c.createPipelineRun(event, pipeline, cluster, pipelineRunStatus.Name, template, params)
		if err != nil {
			return fmt.Errorf("failed to create pipelinerun %s: %s", pipelineRunStatus.Name, err)
		}
		return nil
	}

	unstructured, ok := pipelineRun.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object in pipelineRunLister is not unstructured")
	}
	results, status, startTime, completionTime, err := getPipelineRunStatus(unstructured)
	if err != nil {
		return fmt.Errorf("failed to check pipelineRun status: %s", err)
	}
	if startTime != "" {
		t, err := time.Parse(time.RFC3339, startTime)
		if err != nil {
			return fmt.Errorf("failed to parse startTime %s: %s", startTime, err)
		}
		pipelineRunStatus.StartTime = &metav1.Time{Time: t}
	}
	if completionTime != "" {
		t, err := time.Parse(time.RFC3339, completionTime)
		if err != nil {
			return fmt.Errorf("failed to parse completionTime %s: %s", completionTime, err)
		}
		pipelineRunStatus.CompletionTime = &metav1.Time{Time: t}
	}
	pipelineRunStatus.Results = results

	switch status {
	// PipelineRun completed, no more actions needed.
	case fleetv1alpha2.PipelineRunStatusCompleted, fleetv1alpha2.PipelineRunStatusCancelled, fleetv1alpha2.PipelineRunStatusFailed:
		pipelineRunStatus.Status = status
		event.Status.PipelineRuns[index] = pipelineRunStatus
		return nil
	default:
		// We can cancel the rollback pipeline, too.
		if pipelineRunStatus.Status == fleetv1alpha2.PipelineRunStatusCancelled {
			return c.cancelPipelineRun(event.Namespace, cluster, pipelineRunStatus.Name)
		}
		pipelineRunStatus.Status = status
		event.Status.PipelineRuns[index] = pipelineRunStatus
		return nil
	}
}

func getPipelineRunStatusOrInit(event *fleetv1alpha2.ReleaseEvent, pipeline, cluster string) (bool, int) {
	var index = -1
	for i, status := range event.Status.PipelineRuns {
		if status.Pipeline != pipeline {
			continue
		}
		index = i
		break
	}
	if index == -1 {
		index = len(event.Status.PipelineRuns)
		event.Status.PipelineRuns = append(event.Status.PipelineRuns, fleetv1alpha2.ReleaseEventPipelineRun{
			Pipeline: pipeline,
			Name:     generateNameFunc(event.Name + "-" + pipeline + "-"),
			Cluster:  cluster,
			Status:   fleetv1alpha2.PipelineRunStatusPending,
		})
		return false, index
	}
	return true, index
}

// getPipelineRunStatus returns the status according to:
// https://github.com/tektoncd/pipeline/blob/main/docs/pipelineruns.md#monitoring-execution-status
func getPipelineRunStatus(pipelineRun *unstructured.Unstructured) (map[string]string, fleetv1alpha2.ReleaseEventPipelineRunStatus, string, string, error) {
	startTime, _, err := unstructured.NestedString(pipelineRun.Object, "status", "startTime")
	if err != nil {
		return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.startTime of pipeline %s: %s", pipelineRun.GetName(), err)
	}

	completionTime, _, err := unstructured.NestedString(pipelineRun.Object, "status", "completionTime")
	if err != nil {
		return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.completionTime of pipeline %s: %s", pipelineRun.GetName(), err)
	}

	conditions, found, err := unstructured.NestedSlice(pipelineRun.Object, "status", "conditions")
	if err != nil {
		return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.conditions of pipeline %s: %s", pipelineRun.GetName(), err)
	}
	// There is no `status.conditions` yet. Consider it to be Pending.
	if !found {
		return nil, fleetv1alpha2.PipelineRunStatusPending, startTime, completionTime, nil
	}

	for i, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}
		reason, found, err := unstructured.NestedString(conditionMap, "reason")
		if err != nil {
			return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.conditions[%d].reason of pipeline %s: %s", i, pipelineRun.GetName(), err)
		}
		if !found {
			continue
		}
		if reason == "Started" || reason == "Running" {
			return nil, fleetv1alpha2.PipelineRunStatusRunning, startTime, completionTime, nil
		}
		if reason == "PipelineRunCancelled" {
			return nil, fleetv1alpha2.PipelineRunStatusCancelled, startTime, completionTime, nil
		}

		status, found, err := unstructured.NestedString(conditionMap, "status")
		if err != nil {
			return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.conditions[%d].status of pipeline %s: %s", i, pipelineRun.GetName(), err)
		}
		if !found {
			continue
		}
		if status == "False" {
			return nil, fleetv1alpha2.PipelineRunStatusFailed, startTime, completionTime, nil
		}

		if reason != "Succeeded" && reason != "Completed" {
			continue
		}

		results, found, err := unstructured.NestedSlice(pipelineRun.Object, "status", "pipelineResults")
		if err != nil {
			return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.pipelineResults of pipeline %s: %s", pipelineRun.GetName(), err)
		}
		if !found {
			return nil, fleetv1alpha2.PipelineRunStatusCompleted, startTime, completionTime, nil
		}

		var resultsData = make(map[string]string)
		for index, result := range results {
			resultMap, ok := result.(map[string]interface{})
			if !ok {
				return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.pipelineResults[%d] of pipeline %s: not map[string]interface{}", index, pipelineRun.GetName())
			}
			name, found, err := unstructured.NestedString(resultMap, "name")
			if err != nil {
				return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.pipelineResults[%d].name of pipeline %s: not map[string]interface{}", index, pipelineRun.GetName())
			}
			if !found {
				continue
			}
			value, found, err := unstructured.NestedString(resultMap, "value")
			if err != nil {
				return nil, fleetv1alpha2.PipelineRunStatusUnknown, "", "", fmt.Errorf("failed to look at status.pipelineResults[%d].value of pipeline %s: not map[string]interface{}", index, pipelineRun.GetName())
			}
			if !found {
				continue
			}
			resultsData[name] = value
		}
		return resultsData, fleetv1alpha2.PipelineRunStatusCompleted, startTime, completionTime, nil
	}

	return nil, fleetv1alpha2.PipelineRunStatusUnknown, startTime, completionTime, nil
}
