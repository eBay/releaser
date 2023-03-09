package values

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	"sigs.k8s.io/yaml"

	"github.com/ebay/releaser/pkg/deployer/plugins/kubectl/apis/config"
)

var defaultEnvs = []string{
	"cluster",   // the cluster id where release controller is running
	"namespace", // the namespace where release controller is running
	"cluster_type",
	"zone",
	"region",
	"env",
	"purpose",
	"fcp",
}

func GetEnvDefault(key string) string {
	value := os.Getenv(key)
	if value == "" {
		return "unknown"
	}
	return value
}

type Interface interface {
	Values() (map[string]interface{}, error)
}

type empty struct{}

func (e empty) Values() (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

var Empty Interface = empty{}

type provider struct {
	values           map[string]string
	valuesFiles      []string
	configMapRefs    []config.ObjectReference
	secretRefs       []config.ObjectReference
	defaultNamespace string
	client           kubernetes.Interface

	parseInto func(string, map[string]interface{}) error
}

func New(
	values []string,
	valuesFiles []string,
	configMapRefs []string,
	secretRefs []string,
	defaultNamespace string,
	client kubernetes.Interface,
	parseInto func(string, map[string]interface{}) error,
) Interface {
	m := map[string]string{}
	// Covnert k=v into m[k]=v
	for _, kv := range values {
		sepIndex := strings.Index(kv, "=")
		if sepIndex == -1 {
			continue
		}
		m[kv[:sepIndex]] = kv[sepIndex+1:]
	}
	configMapObjRefs := []config.ObjectReference{}
	for _, configMapRef := range configMapRefs {
		index := strings.Index(configMapRef, "/")
		if index == -1 {
			continue
		}
		namespace, name := configMapRef[:index], configMapRef[index+1:]
		if namespace == "" {
			namespace = defaultNamespace
		}
		configMapObjRefs = append(configMapObjRefs, config.ObjectReference{Namespace: namespace, Name: name})
	}
	secretObjRefs := []config.ObjectReference{}
	for _, secretRef := range secretRefs {
		index := strings.Index(secretRef, "/")
		if index == -1 {
			continue
		}
		namespace, name := secretRef[:index], secretRef[index+1:]
		if namespace == "" {
			namespace = defaultNamespace
		}
		secretObjRefs = append(secretObjRefs, config.ObjectReference{Namespace: namespace, Name: name})
	}

	return &provider{
		values:           m,
		valuesFiles:      valuesFiles,
		configMapRefs:    configMapObjRefs,
		secretRefs:       secretObjRefs,
		defaultNamespace: defaultNamespace,
		client:           client,
		parseInto:        parseInto,
	}
}

func New2(
	values map[string]string,
	valuesFiles []string,
	configMapRefs []config.ObjectReference,
	secretRefs []config.ObjectReference,
	defaultNamespace string,
	client kubernetes.Interface,
	parseInto func(string, map[string]interface{}) error,
) Interface {
	for i, ref := range configMapRefs {
		if ref.Namespace == "" {
			configMapRefs[i].Namespace = defaultNamespace
		}
	}
	for i, ref := range secretRefs {
		if ref.Namespace == "" {
			secretRefs[i].Namespace = defaultNamespace
		}
	}
	return &provider{
		values:           values,
		valuesFiles:      valuesFiles,
		configMapRefs:    configMapRefs,
		secretRefs:       secretRefs,
		defaultNamespace: defaultNamespace,
		client:           client,
		parseInto:        parseInto,
	}
}

func (p *provider) Values() (map[string]interface{}, error) {
	m := map[string]interface{}{}
	// The default environment variables we are looking for.
	for _, env := range defaultEnvs {
		value := os.Getenv(strings.ToUpper(env))
		if value == "" {
			continue
		}
		m[env] = value
	}
	// Then comes to the valuesFiles.
	for _, f := range p.valuesFiles {
		values, err := readFile(os.Expand(f, GetEnvDefault))
		if err != nil {
			return nil, fmt.Errorf("failed to read values from %s: %s", f, err)
		}
		m = MergeMaps(m, values)
	}
	// Convert data from configmap into map.
	for _, ref := range p.configMapRefs {
		configMap, err := p.client.CoreV1().ConfigMaps(ref.Namespace).Get(context.TODO(), ref.Name, metav1.GetOptions{})
		if err != nil {
			klog.Fatalf("failed to read configMap %s/%s: %s", ref.Namespace, ref.Name, err)
		}
		for k, v := range configMap.Data {
			err = p.parseInto(fmt.Sprintf("%s=%s", k, strings.ReplaceAll(v, ",", "\\,")), m)
			if err != nil {
				klog.Fatalf("failed to merge configMap %s/%s at key %s: %s", ref.Namespace, ref.Name, k, err)
			}
		}
	}
	// Convert data from secret into map.
	for _, ref := range p.secretRefs {
		secret, err := p.client.CoreV1().Secrets(ref.Namespace).Get(context.TODO(), ref.Name, metav1.GetOptions{})
		if err != nil {
			klog.Fatalf("failed to read secret %s/%s: %s", ref.Namespace, ref.Name, err)
		}
		for k, v := range secret.Data {
			err = p.parseInto(fmt.Sprintf("%s=%s", k, strings.ReplaceAll(string(v), ",", "\\,")), m)
			if err != nil {
				klog.Fatalf("failed to merge secret %s/%s at key %s: %s", ref.Namespace, ref.Name, k, err)
			}
		}
	}
	// Values has the highest priority
	for k, v := range p.values {
		err := p.parseInto(fmt.Sprintf("%s=%s", k, strings.ReplaceAll(v, ",", "\\,")), m)
		if err != nil {
			klog.Fatalf("failed to merge key %s: %s", k, err)
		}
	}
	return m, nil
}

func readFile(f string) (map[string]interface{}, error) {
	result := map[string]interface{}{}
	err := filepath.Walk(f, func(path string, info os.FileInfo, err error) error {
		if info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %s", path, err)
		}
		values := map[string]interface{}{}
		err = yaml.Unmarshal(data, &values)
		if err != nil {
			return fmt.Errorf("failed to unmarshal %s as values: %s", path, err)
		}
		for k, v := range values {
			result[k] = v
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk %s: %s", f, err)
	}
	return result, nil
}

func MergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v, ok := v.(map[string]interface{}); ok {
			if bv, ok := out[k]; ok {
				if bv, ok := bv.(map[string]interface{}); ok {
					out[k] = MergeMaps(bv, v)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}
