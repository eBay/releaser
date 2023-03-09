package wal

import (
	"io/ioutil"
	"os"

	"k8s.io/klog"
)

var (
	logDir = "/workspace"
)

func Save(queueName, record string) {
	queueLogPath := getQueueLogPath(queueName)
	err := ioutil.WriteFile(queueLogPath, []byte(record), 0644)
	if err != nil {
		klog.Infof("fail to save file: %s", err)
	}
}

func Remove(queueName, target string) {
	queueLogPath := getQueueLogPath(queueName)
	record, err := ioutil.ReadFile(queueLogPath)
	if err != nil {
		klog.Infof("fail to load file: %s", err)
	}

	// remove the log only if current deployed target match the log
	if string(record[:]) == target {
		err = os.Remove(queueLogPath)
		if err != nil {
			klog.Infof("fail to remove file: %s", err)
		}
	}
}

func Load(queueName string) (string, error) {
	queueLogPath := getQueueLogPath(queueName)
	if _, err := os.Stat(queueLogPath); os.IsNotExist(err) {
		return "", nil
	}

	record, err := ioutil.ReadFile(queueLogPath)
	return string(record[:]), err
}

func getQueueLogPath(queueName string) string {
	return logDir + "/releaser-" + queueName + "-queue.log"
}
