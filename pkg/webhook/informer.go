package webhook

import (
	"fmt"
	"net/http"
	"sync"

	"k8s.io/klog"

	"github.com/ebay/releaser/pkg/handler"
)

// Listener is a callback function which is triggered on every event.
type Listener func(string, string, string, string)

// Informer listens client events. For example, git-sync may call this informer
// periodically.
type Informer interface {
	Add(Listener)
	Start()
}

func NewInformer(port int64) Informer {
	return &informer{
		listeners: []Listener{},
		started:   false,
		port:      port,
	}
}

type informer struct {
	lock      sync.Mutex
	listeners []Listener
	started   bool

	port int64
}

func (i *informer) Start() {
	i.lock.Lock()
	i.started = true
	i.lock.Unlock()

	err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", i.port), handler.NewCommitHandler(
		func(commit, tag, version, message string) {
			for _, listener := range i.listeners {
				listener(commit, tag, version, message)
			}
		},
	))
	if err != nil {
		klog.Fatalf("failed to start webhook informer: %s", err)
	}
}

func (i *informer) Add(listener Listener) {
	i.lock.Lock()
	defer i.lock.Unlock()

	// To simplify, stop accepting new listeners once started
	if i.started {
		return
	}
	i.listeners = append(i.listeners, listener)
}
