package wal

import (
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

func init() {
	logDir = "/tmp"
}

func TestQueueRecover(t *testing.T) {
	run(t, true)
}

func initQueue(queue workqueue.RateLimitingInterface) {
	// load queue log
	queueLog, err := Load("test")
	if err != nil {
		klog.Infof("fail to load queue :%s", err)
	}

	if queueLog != "" {
		queue.Add(queueLog)
	}
}

func assertEqual(t *testing.T, a interface{}, b interface{}) {
	if a != b {
		t.Fatalf("%s != %s", a, b)
	}
}

func run(t *testing.T, crash bool) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(1*time.Second, 2*time.Minute), "test")

	// load queue log
	t.Logf("load queue log")
	backupQueueLog, err := Load("test")
	if err != nil {
		t.Logf("fail to load queue :%s", err)
	}

	if !crash {
		assertEqual(t, backupQueueLog, "testLog")
	} else {
		assertEqual(t, backupQueueLog, "")
	}

	if backupQueueLog != "" {
		t.Logf("add log to queue")
		queue.Add(backupQueueLog)
	}

	if crash {
		t.Logf("save queue log")
		Save("test", "testLog")
		queue.Add("testLog")
		time.Sleep(1 * time.Second)
		run(t, false)
		return
	}

	obj, shutdown := queue.Get()

	if shutdown {
		return
	}

	queueLog := obj.(string)
	t.Logf("get log from queue %s", queueLog)
	assertEqual(t, queueLog, "testLog")
	Remove("test", "testLog")
	queue.Done("testLog")
}
