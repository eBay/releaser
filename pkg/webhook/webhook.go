package webhook

import (
	"log"
	"net/http"
	"time"

	"github.com/ebay/releaser/pkg/handler"
)

// Client defines the webhook client.
type Client interface {
	Call(string, string, string)
	CallWithErrorMessage(errMessage string)
}

// New creates a new instance of webhook client
func New(url string, timeout time.Duration, interval time.Duration) Client {
	client := &http.Client{
		Timeout: timeout,
	}
	return &webhook{
		webhookURL: url,
		client:     client,
		interval:   interval,
	}
}

type webhook struct {
	webhookURL string
	client     *http.Client
	interval   time.Duration
	stopCh     chan struct{}
}

func (w *webhook) Call(commit, tag, version string) {
	if w.stopCh != nil {
		close(w.stopCh)
	}
	w.stopCh = make(chan struct{})
	headers := map[string]string{}
	headers[handler.CommitHeader] = commit
	headers[handler.TagHeader] = tag
	headers[handler.VersionHeader] = version

	go w.doCall(headers, w.stopCh)
}

func (w *webhook) CallWithErrorMessage(errMessage string) {
	if w.stopCh != nil {
		close(w.stopCh)
	}
	w.stopCh = make(chan struct{})
	headers := map[string]string{}
	headers[handler.MessageHeader] = errMessage
	w.doCall(headers, w.stopCh)
}

func (w *webhook) doCall(headers map[string]string, stopCh <-chan struct{}) {
	var retry bool
	defer func() {
		select {
		case <-stopCh:
			return
		default:
			if retry {
				time.Sleep(w.interval)
				go w.doCall(headers, stopCh)
			}
		}
	}()

	req, err := http.NewRequest(http.MethodPost, w.webhookURL, nil)
	if err != nil {
		log.Fatalf("failed to new request to %s: %s", w.webhookURL, err)
	}

	for header, val := range headers {
		req.Header.Add(header, val)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		log.Printf("failed to post %s: %s\n", w.webhookURL, err)
		retry = true
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("unexpected status code %d\n", resp.StatusCode)
		retry = true
	}
}
