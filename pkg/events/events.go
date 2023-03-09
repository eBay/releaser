package events

import (
	"encoding/json"
	"fmt"
	"log"
)

// Event is a piece of data that we pass through different components.
type Event struct {
	Commit  string `json:"commit,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

// New creates a new instance of event.
func New(commit, tag, version, path, message string) Event {
	return Event{
		Commit:  commit,
		Tag:     tag,
		Version: version,
		Path:    path,
		Message: message,
	}
}

// String presents the event as a string.
func (e Event) String() string {
	data, err := json.Marshal(&e)
	if err != nil {
		log.Fatalf("failed to marshal event: %s", err)
	}
	return string(data)
}

// From parses the data as an Event.
func From(data string) (Event, error) {
	event := Event{}

	err := json.Unmarshal([]byte(data), &event)
	if err != nil {
		return Event{}, fmt.Errorf("failed to unmarshall as event: %s", err)
	}

	return event, nil
}
