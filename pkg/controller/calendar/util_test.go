package calendar

import (
	"reflect"
	"testing"
	"time"

	fleetv1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestInCalendarHours(t *testing.T) {
	var tests = []struct {
		hours    []fleetv1alpha2.ReleaseCalendarHour
		timeZone intstr.IntOrString
		estimate time.Duration
		hasError bool
	}{
		{

			timeZone: intstr.FromInt(0), // UTC
			hours: []fleetv1alpha2.ReleaseCalendarHour{
				{
					From: 10,
					To:   15,
				},
			},
			// Now is 20:00, we have to wait till 10:00 the next day
			estimate: 14 * time.Hour,
			hasError: false,
		},
		{

			timeZone: intstr.FromInt(0), // UTC
			hours: []fleetv1alpha2.ReleaseCalendarHour{
				{
					From: 15,
					To:   21, // 21:00pm the second day
				},
			},
			estimate: 0,
			hasError: false,
		},
		{

			timeZone: intstr.FromInt(0), // UTC
			hours: []fleetv1alpha2.ReleaseCalendarHour{
				{
					From: 22,
					To:   5, // 21:00pm the second day
				},
			},
			estimate: 2 * time.Hour,
			hasError: false,
		},
		{

			timeZone: intstr.FromInt(0), // UTC
			hours: []fleetv1alpha2.ReleaseCalendarHour{
				{
					From: 22,
					To:   45, // 21:00pm the second day
				},
			},
			estimate: 0,
			hasError: false,
		},
	}

	for i, test := range tests {
		estimate, _, err := inCalendarHours(nowFunc().Time, test.hours, test.timeZone)
		if estimate != test.estimate {
			t.Errorf("[%d] expected estimate %s, but see %s", i, test.estimate.String(), estimate.String())
		}
		if test.hasError && err == nil {
			t.Errorf("[%d]: expect error, but got nil", i)
		}
		if !test.hasError && err != nil {
			t.Errorf("[%d]: unexpected error: %s", i, err)
		}
	}
}

func TestRunAfter(t *testing.T) {
	var tests = []struct {
		event    *fleetv1alpha2.ReleaseEvent
		runAfter []fleetv1alpha2.ReleaseEventRunAfter
	}{
		{
			event: &fleetv1alpha2.ReleaseEvent{
				Spec: fleetv1alpha2.ReleaseEventSpec{
					RunAfter: []fleetv1alpha2.ReleaseEventRunAfter{
						{
							Name: "foo",
						},
					},
				},
			},
			runAfter: []fleetv1alpha2.ReleaseEventRunAfter{
				{
					Name: "foo",
				},
			},
		},
	}

	for i, test := range tests {
		runAfter := runAfter(test.event)
		if !reflect.DeepEqual(test.runAfter, runAfter) {
			t.Errorf("[%d]: unexpected results: %v", i, runAfter)
		}
	}
}

func TestLookupYAML(t *testing.T) {
	var tests = []struct {
		data  map[string]string
		key   string
		found bool
		value string
	}{
		{
			data: map[string]string{
				"hello": "world",
			},
			key:   "hello",
			found: true,
			value: "world",
		},
		{
			data: map[string]string{
				"hello": "world",
			},
			key:   "hello.yaml",
			found: false,
		},
		{
			data: map[string]string{
				"hello.yaml": "world",
			},
			key:   "hello",
			found: true,
			value: "world",
		},
		{
			data: map[string]string{
				"hello.yaml": "world",
			},
			key:   "hello1",
			found: false,
		},
	}
	for i, test := range tests {
		value, found := lookupYAML(test.data, test.key)
		if found && !test.found {
			t.Errorf("[%d]: expect to see not found", i)
		} else if test.found && !found {
			t.Errorf("[%d]: expect to see found", i)
		}

		if value != test.value {
			t.Errorf("[%d]: expect to see %s, but got %s", i, test.value, value)
		}
	}
}

func TestFitsInForward(t *testing.T) {
	var tests = []struct {
		when     time.Time
		weekdays []fleetv1alpha2.ReleaseCalendarDay
		skipDays []string
		expected time.Time
	}{
		{
			when:     time.Date(2022, 7, 1, 0, 0, 0, 0, time.Local),
			expected: time.Date(2022, 7, 1, 0, 0, 0, 0, time.Local),
		},
		{
			when: time.Date(2022, 7, 1, 0, 0, 0, 0, time.Local),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			skipDays: []string{"2022-07-04"},
			expected: time.Date(2022, 7, 5, 0, 0, 0, 0, time.Local),
		},
	}

	for i, test := range tests {
		when := fitsInForward(test.when, test.weekdays, test.skipDays)
		if when.Unix() != test.expected.Unix() {
			t.Errorf("[%d]: unexpected date %s", i, when.String())
		}
	}
}

func TestFitsInBackward(t *testing.T) {
	var tests = []struct {
		when     time.Time
		weekdays []fleetv1alpha2.ReleaseCalendarDay
		skipDays []string
		expected time.Time
	}{
		{
			when:     time.Date(2022, 7, 1, 0, 0, 0, 0, time.Local),
			expected: time.Date(2022, 7, 1, 0, 0, 0, 0, time.Local),
		},
		{
			when: time.Date(2022, 7, 2, 0, 0, 0, 0, time.Local),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			expected: time.Date(2022, 6, 30, 0, 0, 0, 0, time.Local),
		},
		{
			when: time.Date(2022, 7, 4, 0, 0, 0, 0, time.Local),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			skipDays: []string{"2022-07-04"},
			expected: time.Date(2022, 6, 30, 0, 0, 0, 0, time.Local),
		},
	}

	for i, test := range tests {
		when := fitsInBackward(test.when, test.weekdays, test.skipDays)
		if when.Unix() != test.expected.Unix() {
			t.Errorf("[%d]: unexpected date %s", i, when.String())
		}
	}
}

func TestGetTimeWithOffset(t *testing.T) {
	var tests = []struct {
		time     time.Time
		offset   string
		timeZone intstr.IntOrString
		weekdays []fleetv1alpha2.ReleaseCalendarDay
		skipDays []string
		result   time.Time
		hasError bool
	}{
		{
			time:     time.Date(2022, 4, 5, 10, 0, 0, 0, time.UTC),
			offset:   "-2h30m",
			timeZone: intstr.FromInt(0),
			result:   time.Date(2022, 4, 5, 7, 30, 0, 0, time.UTC),
			hasError: false,
		},
		{
			time:     time.Date(2022, 4, 5, 6, 0, 0, 0, time.UTC),
			offset:   "-24h,10:00",
			timeZone: intstr.FromInt(-7),
			result:   time.Date(2022, 4, 3, 17, 0, 0, 0, time.UTC),
			hasError: false,
		},
		{
			time:     time.Date(2022, 7, 11, 6, 0, 0, 0, time.UTC),
			offset:   "-24h,10:00",
			timeZone: intstr.FromInt(-7),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			result:   time.Date(2022, 7, 7, 17, 0, 0, 0, time.UTC),
			hasError: false,
		},
		{
			time:     time.Date(2022, 7, 11, 6, 0, 0, 0, time.UTC),
			offset:   "-48h,10:00",
			timeZone: intstr.FromInt(-7),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			result:   time.Date(2022, 7, 6, 17, 0, 0, 0, time.UTC),
			hasError: false,
		},
		{
			time:     time.Date(2022, 7, 7, 13, 0, 0, 0, time.UTC),
			offset:   "24h,10:00",
			timeZone: intstr.FromInt(-7),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			result:   time.Date(2022, 7, 11, 17, 0, 0, 0, time.UTC),
			hasError: false,
		},
		{
			time:     time.Date(2022, 7, 7, 13, 0, 0, 0, time.UTC),
			offset:   "48h,10:00",
			timeZone: intstr.FromInt(-7),
			weekdays: []fleetv1alpha2.ReleaseCalendarDay{
				{
					From: 1,
					To:   4,
				},
			},
			result:   time.Date(2022, 7, 12, 17, 0, 0, 0, time.UTC),
			hasError: false,
		},
	}

	for i, test := range tests {
		result, err := getTimeWithOffset(&metav1.Time{Time: test.time}, test.offset, test.timeZone, test.weekdays, test.skipDays)
		if !test.hasError && err != nil {
			t.Errorf("[%d] unexpected error %s", i, err)
		} else if test.hasError && err == nil {
			t.Errorf("[%d] expected to see error, but got nil", i)
		}

		if result.Unix() != test.result.Unix() {
			t.Errorf("[%d] expected to see %s, but got %s", i, test.result.Format(time.RFC3339), result.Format(time.RFC3339))
		}
	}
}

func TestPipelineGroups(t *testing.T) {
	var tests = []struct {
		pipelines []fleetv1alpha2.ReleaseCalendarPipeline
		begins    []string
		ends      []string
	}{
		{
			pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:      "cr",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
				{
					Name:      "stat",
					RunPolicy: "",
				},
				{
					Name:      "rollout",
					RunPolicy: fleetv1alpha2.RunPolicyAlways,
				},
				{
					Name:      "close",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
			},
			begins: []string{"cr", "stat", "stat", "close"},
			ends:   []string{"cr", "rollout", "rollout", "close"},
		},
		{
			pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:      "foo",
					RunPolicy: fleetv1alpha2.RunPolicyAlways,
				},
				{
					Name:      "bar",
					RunPolicy: "",
				},
				{
					Name:      "cat",
					RunPolicy: fleetv1alpha2.RunPolicyAlways,
				},
			},
			begins: []string{"foo", "foo", "foo"},
			ends:   []string{"cat", "cat", "cat"},
		},
		{
			pipelines: []fleetv1alpha2.ReleaseCalendarPipeline{
				{
					Name:      "a",
					RunPolicy: fleetv1alpha2.RunPolicyAlways,
				},
				{
					Name:      "b",
					RunPolicy: "",
				},
				{
					Name:      "c",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
				{
					Name:      "d",
					RunPolicy: fleetv1alpha2.RunPolicyAlways,
				},
				{
					Name:      "e",
					RunPolicy: fleetv1alpha2.RunPolicyAlways,
				},
				{
					Name:      "f",
					RunPolicy: fleetv1alpha2.RunPolicyLeaderOnly,
				},
			},
			begins: []string{"a", "a", "c", "d", "d", "f"},
			ends:   []string{"b", "b", "c", "e", "e", "f"},
		},
	}

	for i, test := range tests {
		begins, ends, _ := pipelineGroups(test.pipelines)
		if !equality.Semantic.DeepEqual(test.begins, begins) {
			t.Errorf("[%d] unexpected begins\nDiff:\n%s", i, diff.ObjectGoPrintDiff(test.begins, begins))
		}
		if !equality.Semantic.DeepEqual(test.ends, ends) {
			t.Errorf("[%d] unexpected ends\nDiff:\n%s", i, diff.ObjectGoPrintDiff(test.ends, ends))
		}
	}
}

func TestInSameDay(t *testing.T) {
	var tests = []struct {
		time0     metav1.Time
		time1     metav1.Time
		timeZone  intstr.IntOrString
		inSameDay bool
	}{
		{
			time0:     metav1.Time{Time: time.Date(2022, 7, 13, 17, 0, 0, 0, time.UTC)},
			time1:     metav1.Time{Time: time.Date(2022, 7, 13, 19, 0, 0, 0, time.UTC)},
			timeZone:  intstr.FromString("America/Los_Angeles"),
			inSameDay: true,
		},
		{
			time0:     metav1.Time{Time: time.Date(2022, 7, 13, 17, 0, 0, 0, time.UTC)},
			time1:     metav1.Time{Time: time.Date(2022, 7, 13, 21, 0, 0, 0, time.UTC)},
			timeZone:  intstr.FromString("America/Los_Angeles"),
			inSameDay: true,
		},
		{
			time0:     metav1.Time{Time: time.Date(2022, 7, 13, 21, 0, 0, 0, time.UTC)},
			time1:     metav1.Time{Time: time.Date(2022, 7, 13, 23, 0, 0, 0, time.UTC)},
			timeZone:  intstr.FromString("America/Los_Angeles"),
			inSameDay: true,
		},
		{
			time0:     metav1.Time{Time: time.Date(2022, 7, 13, 6, 0, 0, 0, time.UTC)},
			time1:     metav1.Time{Time: time.Date(2022, 7, 13, 9, 0, 0, 0, time.UTC)},
			timeZone:  intstr.FromString("America/Los_Angeles"),
			inSameDay: false,
		},
	}
	for i, test := range tests {
		loc, err := locationOf(test.timeZone)
		if err != nil {
			t.Errorf("parse timezone: %s error %s", test.timeZone.String(), err)
		}
		if test.inSameDay != inSameDay(test.time0.Time, test.time1.Time, loc) {
			t.Errorf("inSameDay mismatch %d", i)
		}
	}
}
