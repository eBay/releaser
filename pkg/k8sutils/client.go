package k8sutils

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
)

const (
	ServerCertPath = "/var/run/kubernetes/server.crt"
	ServerKeyPath  = "/var/run/kubernetes/server.key"
)

var (
	p    = &proxy{}
	once = sync.Once{}
)

type proxy struct {
	lock     sync.RWMutex
	backends map[string]http.Handler
	indexes  map[string]int
	index    int
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	handler, ok := p.backends[r.Host]
	if !ok {
		http.NotFound(w, r)
		return
	}

	handler.ServeHTTP(w, r)
}

func (p *proxy) addIfNotExist(targetHost string, handler http.Handler) int {
	p.lock.Lock()
	defer p.lock.Unlock()

	if p.backends == nil {
		p.backends = make(map[string]http.Handler)
	}
	if p.indexes == nil {
		p.indexes = make(map[string]int)
	}

	index, ok := p.indexes[targetHost]
	if ok {
		return index
	}
	index = p.index + 1

	p.backends[fmt.Sprintf("127.0.0.%d:6443", index)] = handler
	p.indexes[targetHost] = index

	return index
}

func ConfigFrom(kubeconfig, kubecontext string) *rest.Config {
	if kubeconfig == "" {
		log.Fatalf("kubeconfig is not set")
	}
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).ClientConfig()
	if err != nil {
		log.Fatalf("failed to load rest config: %s", err)
	}

	// the host field might be on http(plain text), and due to the fact that
	// client-go skips credentials on http, we need to launch an in-process
	// reverse proxy on https
	if !strings.HasPrefix(config.Host, "http://") {
		return config
	}
	targetURL, err := url.Parse(config.Host)
	if err != nil {
		log.Fatalf("failed to parse %s: %s", config.Host, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = targetURL.Host
	}

	index := p.addIfNotExist(config.Host, proxy)

	once.Do(func() {
		pemCert, pemKey, err := cert.GenerateSelfSignedCertKey("localhost", nil, nil)
		if err != nil {
			log.Fatalf("failed to generate self signed certificate and key: %s", err)
		}
		if err := cert.WriteCert(ServerCertPath, pemCert); err != nil {
			log.Fatalf("failed to write certificate: %s", err)
		}
		if err := keyutil.WriteKey(ServerKeyPath, pemKey); err != nil {
			log.Fatalf("failed to write key: %s", err)
		}

		// start the reserve proxy on https
		go func() {
			http.ListenAndServeTLS("0.0.0.0:6443", ServerCertPath, ServerKeyPath, p)
		}()
	})

	overrides.ClusterInfo.CertificateAuthority = ServerCertPath
	overrides.ClusterInfo.Server = fmt.Sprintf("https://127.0.0.%d:6443", index)
	overrides.ClusterInfo.TLSServerName = "localhost"
	config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).ClientConfig()
	if err != nil {
		log.Fatalf("failed to load rest config with proxy overrides: %s", err)
	}

	return config
}

// ListContexts gives all the context names as specified in the given kubeconfig.
func ListContexts(kubeconfig string) []string {
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	overrides := &clientcmd.ConfigOverrides{}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).RawConfig()
	if err != nil {
		log.Fatalf("failed to load raw config: %s", err)
	}

	contexts := []string{}
	for name := range config.Contexts {
		contexts = append(contexts, name)
	}
	return contexts
}
