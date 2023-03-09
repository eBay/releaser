package handler

import "net/http"

const (
	// CommitHeader is the header key for commit information.
	CommitHeader = "X-RELEASER-COMMIT"
	// TagHeader is the header key for tag information.
	TagHeader = "X-RELEASER-TAG"
	// VersionHeader is the header key for version information.
	VersionHeader = "X-RELEASER-VERSION"
	// MessageHeader is the header key for message information.
	MessageHeader = "X-RELEASER-MESSAGE"
)

// OnCommit is a callback function to receive the commit and version information.
type OnCommit func(string, string, string, string)

// NewCommitHandler creates a new handler to extrace commit and version from
// request header, and invokes oncommit callback.
func NewCommitHandler(onCommit OnCommit) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		onCommit(r.Header.Get(CommitHeader), r.Header.Get(TagHeader), r.Header.Get(VersionHeader), r.Header.Get(MessageHeader))
	}
}
