package mitm

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"cursor-client/internal/certs"

	"github.com/elazarl/goproxy"
)

// ProxyServer represents the MITM proxy server
type ProxyServer struct {
	addr     string
	ca       *certs.CA
	proxy    *goproxy.ProxyHttpServer
	listener net.Listener
	mu       sync.RWMutex
	running  bool

	// Handler for intercepted requests
	handler http.Handler
}

type responseModifier interface {
	ModifyResponse(resp *http.Response, req *http.Request) *http.Response
}

type requestInterceptor interface {
	InterceptRequest(req *http.Request) (*http.Response, bool)
}

// Config holds proxy server configuration
type Config struct {
	Addr    string       // Listen address (e.g., "127.0.0.1:18080")
	Handler http.Handler // Handler for intercepted requests
	CA      *certs.CA    // Optional persisted CA
}

// NewProxyServer creates a new MITM proxy server
func NewProxyServer(cfg Config) (*ProxyServer, error) {
	ca := cfg.CA
	if ca == nil {
		var err error
		ca, err = certs.NewCA()
		if err != nil {
			return nil, fmt.Errorf("failed to create CA: %w", err)
		}
	}

	// Create goproxy instance
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false

	ps := &ProxyServer{
		addr:    cfg.Addr,
		ca:      ca,
		proxy:   proxy,
		handler: cfg.Handler,
	}

	// Set up MITM for HTTPS
	ps.setupMITM()

	return ps, nil
}

// setupMITM configures HTTPS interception
func (ps *ProxyServer) setupMITM() {
	// Intercept all requests
	ps.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Add custom headers for tracking
		req.Header.Set("X-Proxy-By", "Cursor-Assistant")

		// Extract original server info
		if req.URL.Scheme == "" {
			req.URL.Scheme = "https"
		}
		if req.URL.Host == "" {
			req.URL.Host = req.Host
		}

		// Store original URL in header
		req.Header.Set("X-Raw-Cursor-Server-URL", req.URL.String())

		// Highlight Cursor API requests
		if strings.Contains(req.Host, "cursor.sh") || strings.Contains(req.Host, "cursor.com") {
			log.Printf("[MITM] 🎯 CURSOR API: %s %s", req.Method, req.URL.String())
		} else {
			log.Printf("[MITM] Intercepted: %s %s", req.Method, req.URL.String())
		}
		if interceptor, ok := ps.handler.(requestInterceptor); ok {
			if resp, handled := interceptor.InterceptRequest(req); handled {
				return req, resp
			}
		}

		return req, nil
	})

	// Intercept responses
	ps.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp != nil {
			log.Printf("[MITM] Response: %d %s", resp.StatusCode, ctx.Req.URL.String())
		}
		if modifier, ok := ps.handler.(responseModifier); ok && resp != nil && ctx != nil {
			return modifier.ModifyResponse(resp, ctx.Req)
		}
		return resp
	})

	// Set up dynamic certificate generation
	ps.proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		// Generate certificate for this host
		return &goproxy.ConnectAction{
			Action: goproxy.ConnectMitm,
			TLSConfig: func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
				return ps.getTLSConfig(host), nil
			},
		}, host
	})
}

// getTLSConfig generates TLS config with dynamic certificate for hostname
func (ps *ProxyServer) getTLSConfig(hostname string) *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			// Generate certificate for requested hostname
			name := hello.ServerName
			if name == "" {
				name = stripHostPort(hostname)
			}
			cert, err := ps.ca.GenerateServerCert(name)
			if err != nil {
				log.Printf("[MITM] Failed to generate cert for %s: %v", name, err)
				return nil, err
			}
			return cert, nil
		},
	}
}

func stripHostPort(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// Start starts the proxy server
func (ps *ProxyServer) Start() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.running {
		return fmt.Errorf("proxy already running")
	}

	// Create listener
	listener, err := net.Listen("tcp", ps.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", ps.addr, err)
	}

	ps.listener = listener
	ps.running = true

	log.Printf("[MITM] Proxy server started on %s", ps.addr)

	// Start serving in background
	go func() {
		if err := http.Serve(listener, ps.proxy); err != nil {
			if !ps.isRunning() {
				return // Server was stopped intentionally
			}
			log.Printf("[MITM] Server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the proxy server
func (ps *ProxyServer) Stop() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.running {
		return fmt.Errorf("proxy not running")
	}

	if ps.listener != nil {
		if err := ps.listener.Close(); err != nil {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	ps.running = false
	log.Printf("[MITM] Proxy server stopped")

	return nil
}

// IsRunning returns whether the proxy is running
func (ps *ProxyServer) IsRunning() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.running
}

func (ps *ProxyServer) isRunning() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.running
}

// GetCA returns the CA certificate
func (ps *ProxyServer) GetCA() *certs.CA {
	return ps.ca
}

// GetCAPEM returns CA certificate in PEM format
func (ps *ProxyServer) GetCAPEM() ([]byte, error) {
	certPEM, _, err := ps.ca.ToPEM()
	return certPEM, err
}
