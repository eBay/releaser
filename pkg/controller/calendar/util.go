package calendar

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilrand "k8s.io/apimachinery/pkg/util/rand"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
)

// copied from k8s.io/apiserver/pkg/storage/names/generator.go
const (
	// TODO: make this flexible for non-core resources with alternate naming rules.
	maxNameLength          = 63
	randomLength           = 5
	maxGeneratedNameLength = maxNameLength - randomLength
)

// GenerateName is a function which appends random characters to the base name.
func GenerateName(base string) string {
	if len(base) > maxGeneratedNameLength {
		base = base[:maxGeneratedNameLength]
	}
	return fmt.Sprintf("%s%s", base, utilrand.String(randomLength))
}

func NameOfEvent(event *fleetv1alpha2.ReleaseEvent) string {
	name, ok := event.Labels["name"]
	if ok {
		return name
	}

	if event.GenerateName != "" {
		return strings.Trim(event.GenerateName, "-")
	} else if strings.HasPrefix(event.Name, event.Spec.Calendar) {
		return strings.Trim(strings.TrimPrefix(event.Name, event.Spec.Calendar), "-")
	}
	return event.Name
}

func getTimeWithOffset(t *metav1.Time, offset string, timeZone intstr.IntOrString, weekdays []fleetv1alpha2.ReleaseCalendarDay, skipDays []string) (time.Time, error) {
	if offset == "" {
		return t.Time, nil
	}

	loc, err := locationOf(timeZone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone: %s", err)
	}
	locTime := t.Time.In(loc)

	offsets := strings.Split(offset, ",")

	duration, err := time.ParseDuration(offsets[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse %s as duration: %s", offsets[0], err)
	}
	if duration > 0 {
		// move forward
		for duration > 0 {
			if duration > 24*time.Hour {
				locTime = locTime.Add(24 * time.Hour)
			} else {
				locTime = locTime.Add(duration)
			}
			locTime = fitsInForward(locTime, weekdays, skipDays)
			duration = duration - 24*time.Hour
		}
	} else {
		// move backward
		for duration < 0 {
			if duration < -24*time.Hour {
				locTime = locTime.Add(-24 * time.Hour)
			} else {
				locTime = locTime.Add(duration)
			}
			locTime = fitsInBackward(locTime, weekdays, skipDays)
			duration = duration + 24*time.Hour
		}
	}

	if len(offsets) > 1 {
		hm, err := time.Parse("15:04", offsets[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to parse %s as time hh:mm: %s", offsets[1], err)
		}
		return time.Date(locTime.Year(), locTime.Month(), locTime.Day(), hm.Hour(), hm.Minute(), 0, 0, loc), nil
	}

	return locTime, nil
}

func inCalendarHours(t time.Time, hours []fleetv1alpha2.ReleaseCalendarHour, timeZone intstr.IntOrString) (time.Duration, map[string]string, error) {
	if len(hours) == 0 {
		return 0, nil, nil
	}

	var estimate time.Duration = time.Hour * 24

	loc, err := locationOf(timeZone)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid timezone: %s", err)
	}
	t = t.In(loc)

	for _, hour := range hours {
		values := make(map[string]string)
		for _, param := range hour.Params {
			values[param.Name] = param.Value
		}
		if t.Hour() < int(hour.From) {
			if hour.To > 24 && t.Hour() < (int(hour.To)-24) {
				return 0, values, nil
			}
			newEstimate := time.Date(t.Year(), t.Month(), t.Day(), int(hour.From), 0, 0, 0, loc).Sub(t)
			if newEstimate < estimate {
				estimate = newEstimate
			}
			continue
		} else {
			if t.Hour() < int(hour.To) {
				return 0, values, nil
			}
			newEstimate := time.Date(t.Year(), t.Month(), t.Day()+1, int(hour.From), 0, 0, 0, loc).Sub(t)
			if newEstimate < estimate {
				estimate = newEstimate
			}
		}
	}
	return estimate, nil, nil
}

// pipelineGroups returns two same length arrays:
//
// 1. begins: denotes the first Always pipeline in a consecutive range.
// 2. ends: denotes the last Always pipeline in a consecutive range.
//
// Notations: L -> LeaderOnly, A -> Always
//
// 1. [A, A, A] => [2, 2, 2], [0, 0, 0]
// 2. [L, A, A, L] => [0, 2, 2, 3], [0, 1, 1, 2]
// 3. [L, A, L, A] => [0, 1, 2, 3], [0, 1, 2, 3]
func pipelineGroups(pipelines []fleetv1alpha2.ReleaseCalendarPipeline) ([]string, []string, map[string]int) {
	begins, ends := make([]string, len(pipelines)), make([]string, len(pipelines))
	indexes := map[string]int{}

	var bname string
	for i := 0; i < len(pipelines); i++ {
		indexes[pipelines[i].Name] = i

		if pipelines[i].RunPolicy == fleetv1alpha2.RunPolicyLeaderOnly {
			begins[i] = pipelines[i].Name
			bname = ""
		} else {
			if bname == "" {
				bname = pipelines[i].Name
			}
			begins[i] = bname
		}
	}

	var ename string
	for i := len(pipelines) - 1; i >= 0; i-- {
		if pipelines[i].RunPolicy == fleetv1alpha2.RunPolicyLeaderOnly {
			ends[i] = pipelines[i].Name
			ename = ""
		} else {
			if ename == "" {
				ename = pipelines[i].Name
			}
			ends[i] = ename
		}
	}

	return begins, ends, indexes
}

// filterEvents looks for all the events. If there are duplicated events whose
// name label is the same, then the event with a newer status.time is selected.
func filterEvents(events []*fleetv1alpha2.ReleaseEvent) []*fleetv1alpha2.ReleaseEvent {
	var results = []*fleetv1alpha2.ReleaseEvent{}
	var byName = make(map[string]*fleetv1alpha2.ReleaseEvent)
	for _, event := range events {
		name, ok := event.Labels["name"]
		if !ok {
			continue
		}

		if event.Status.Time == nil {
			results = append(results, event.DeepCopy())
			continue
		}

		existing, ok := byName[name]
		if !ok {
			byName[name] = event.DeepCopy()
			continue
		}
		if event.Status.Time.After(existing.Status.Time.Time) {
			byName[name] = event.DeepCopy()
		}
	}
	for _, event := range byName {
		results = append(results, event)
	}
	return results
}

func inSameDay(time1 time.Time, time2 time.Time, location *time.Location) bool {
	return time1.In(location).Format("2006-01-02") == time2.In(location).Format("2006-01-02")
}

func locationOf(timeZone intstr.IntOrString) (*time.Location, error) {
	if timeZone.Type == intstr.Int {
		return time.FixedZone(fmt.Sprintf("%d", timeZone.IntValue()), 60*60*timeZone.IntValue()), nil
	}
	return time.LoadLocation(timeZone.String())
}

// runAfter merges spec.after and spec.runAfter.
// TODO: remove this once spec.after is removed.
func runAfter(event *fleetv1alpha2.ReleaseEvent) []fleetv1alpha2.ReleaseEventRunAfter {
	return event.Spec.RunAfter
}

// lookupYAML finds the key in a map and adds `.yaml` suffix to key if not found.
func lookupYAML(data map[string]string, key string) (string, bool) {
	value, ok := data[key]
	if ok {
		return value, true
	}
	if strings.HasSuffix(key, ".yaml") {
		return "", false
	}
	value, ok = data[key+".yaml"]
	if ok {
		return value, true
	}
	return "", false
}

func fitsInForward(when time.Time, weekdays []fleetv1alpha2.ReleaseCalendarDay, skipDays []string) time.Time {
	if len(weekdays) == 0 && len(skipDays) == 0 {
		return when
	}

	var fit bool
	for _, weekday := range weekdays {
		if when.Weekday() >= time.Weekday(weekday.From) && when.Weekday() <= time.Weekday(weekday.To) {
			fit = true
			break
		}
	}
	if !fit {
		// Try the next day.
		return fitsInForward(when.Add(24*time.Hour), weekdays, skipDays)
	}

	date := fmt.Sprintf("%d-%02d-%02d", when.Year(), when.Month(), when.Day())
	for _, day := range skipDays {
		if date == day {
			// Try the next day.
			return fitsInForward(when.Add(24*time.Hour), weekdays, skipDays)
		}
	}

	return when
}

func fitsInBackward(when time.Time, weekdays []fleetv1alpha2.ReleaseCalendarDay, skipDays []string) time.Time {
	if len(weekdays) == 0 && len(skipDays) == 0 {
		return when
	}

	var fit bool
	for _, weekday := range weekdays {
		if when.Weekday() >= time.Weekday(weekday.From) && when.Weekday() <= time.Weekday(weekday.To) {
			fit = true
			break
		}
	}
	if !fit {
		// Try the previous day.
		return fitsInBackward(when.Add(-24*time.Hour), weekdays, skipDays)
	}

	date := fmt.Sprintf("%d-%02d-%02d", when.Year(), when.Month(), when.Day())
	for _, day := range skipDays {
		if date == day {
			// Try the previous day.
			return fitsInBackward(when.Add(-24*time.Hour), weekdays, skipDays)
		}
	}

	return when
}

func pipelineCompleted(ev *fleetv1alpha2.ReleaseEvent, pipeline string) bool {
	if pipeline == "" {
		return ev.Status.Phase == fleetv1alpha2.EventStatusCompleted
	}
	var completed = false
	for _, run := range ev.Status.PipelineRuns {
		if run.Pipeline != pipeline {
			continue
		}
		if run.Status == fleetv1alpha2.PipelineRunStatusCompleted {
			completed = true
		}
		break
	}
	return completed
}
