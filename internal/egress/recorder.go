// Package egress records outbound HTTP(S) a test suite attempts, so a
// suite can fail when it reaches past loopback.
//
// A Recorder is an HTTP proxy on loopback that refuses everything it
// is sent and remembers the destination. A suite points HTTPS_PROXY and
// HTTP_PROXY at it before any request is made: Go's default transport
// then hands every non-loopback request to the recorder instead of the
// network, and so does any foo binary the suite starts with that
// environment. Loopback traffic (httptest servers, local models) never
// goes through a proxy and is not seen.
package egress

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
)

// Recorder is a refusing, recording HTTP proxy. The zero value is not
// usable; call Start.
type Recorder struct {
	ln   net.Listener
	mu   sync.Mutex
	seen map[string]int
	wg   sync.WaitGroup
}

// Start listens on a loopback port and serves until Stop.
func Start() (*Recorder, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &Recorder{ln: ln, seen: map[string]int{}}
	r.wg.Add(1)
	go r.serve()
	return r, nil
}

// URL is the proxy address, for HTTPS_PROXY / HTTP_PROXY.
func (r *Recorder) URL() string { return "http://" + r.ln.Addr().String() }

// Install points the process's proxy environment at r: HTTPS_PROXY and
// HTTP_PROXY set, their lowercase forms and NO_PROXY cleared so neither
// can route around it. Call before the first request: net/http reads
// the proxy environment once.
func (r *Recorder) Install() {
	for _, k := range []string{"https_proxy", "http_proxy", "no_proxy", "NO_PROXY"} {
		_ = os.Unsetenv(k)
	}
	_ = os.Setenv("HTTPS_PROXY", r.URL())
	_ = os.Setenv("HTTP_PROXY", r.URL())
}

// Stop closes the listener and returns every destination seen, as
// "host:port" (CONNECT) or the request URL (plain HTTP), each with its
// attempt count, sorted.
func (r *Recorder) Stop() []string {
	_ = r.ln.Close()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for dest, n := range r.seen {
		out = append(out, fmt.Sprintf("%s (%d)", dest, n))
	}
	sort.Strings(out)
	return out
}

func (r *Recorder) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(c)
	}
}

func (r *Recorder) handle(c net.Conn) {
	defer c.Close()
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return
	}
	dest := req.Host
	if req.Method != http.MethodConnect {
		dest = req.URL.String()
	}
	r.mu.Lock()
	r.seen[dest]++
	r.mu.Unlock()
	_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
}
