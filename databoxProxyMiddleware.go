package main

import (
	"crypto/tls"
	"crypto/x509"
	"time"

	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	libDatabox "github.com/me-box/lib-go-databox"
)

type DataboxProxyMiddleware struct {
	mutex             sync.Mutex
	httpClient        *http.Client
	next              http.Handler
	databoxCACertPool *x509.CertPool
}

func NewProxyMiddleware(rootCertPath string) *DataboxProxyMiddleware {

	var h *http.Client
	var roots *x509.CertPool
	if rootCertPath != "" {
		h = libDatabox.NewDataboxHTTPsAPIWithPaths(rootCertPath)
		CM_HTTPS_CA_ROOT_CERT, _ := ioutil.ReadFile(rootCertPath)
		roots = x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(CM_HTTPS_CA_ROOT_CERT))
	}

	d := &DataboxProxyMiddleware{
		mutex:             sync.Mutex{},
		httpClient:        h,
		databoxCACertPool: roots,
	}

	return d
}

func (d *DataboxProxyMiddleware) ProxyMiddleware(next http.Handler) http.Handler {

	proxy := func(w http.ResponseWriter, r *http.Request) {

		if websocket.IsWebSocketUpgrade(r) {
			d.proxyWebSocket(w, r, next)
		} else {
			d.proxyHTTP(w, r, next)
		}

	}

	return http.HandlerFunc(proxy)
}

func (d *DataboxProxyMiddleware) proxyWebSocket(w http.ResponseWriter, r *http.Request, next http.Handler) {

	libDatabox.Debug("[proxyWebSocket] started")

	parts := strings.Split(r.URL.Path, "/")

	//lets proxy all ui request for now
	//proxy is bit of a hack for now but the proxy is moving to the core-network at some point soon
	/*if _, ok := d.proxyList[parts[1]]; ok == false {
		//no need to proxy
		next.ServeHTTP(w, r)
		return
	}*/
	if len(parts) < 3 || parts[2] != "ui" {
		//no need to proxy
		next.ServeHTTP(w, r)
		return
	}

	backendURL := "wss://" + parts[1] + ":8080/" + strings.Join(parts[2:], "/")

	libDatabox.Debug("[proxyWebSocket] proxing to " + backendURL)

	dialer := &websocket.Dialer{
		TLSClientConfig: &tls.Config{
			RootCAs: d.databoxCACertPool,
		},
		HandshakeTimeout:  5 * time.Second,
		EnableCompression: false,
	}

	// Pass headers from the incoming request to the dialer to forward them to
	// the final destinations.
	requestHeader := http.Header{}
	if origin := r.Header.Get("Origin"); origin != "" {
		requestHeader.Add("Origin", origin)
	}
	for _, prot := range r.Header[http.CanonicalHeaderKey("Sec-WebSocket-Protocol")] {
		requestHeader.Add("Sec-WebSocket-Protocol", prot)
	}
	for _, cookie := range r.Header[http.CanonicalHeaderKey("Cookie")] {
		requestHeader.Add("Cookie", cookie)
	}

	connBackend, resp, err := dialer.Dial(backendURL, requestHeader)
	if err != nil {
		libDatabox.Debug("[ERROR proxyWebSocket] Could not open websocket connection to backend " + err.Error())
		http.Error(w, "Could not open websocket connection to backend", http.StatusBadRequest)
		return
	}
	defer connBackend.Close()

	upgradeHeader := http.Header{}
	if hdr := resp.Header.Get("Sec-Websocket-Protocol"); hdr != "" {
		upgradeHeader.Set("Sec-Websocket-Protocol", hdr)
	}
	if hdr := resp.Header.Get("Set-Cookie"); hdr != "" {
		upgradeHeader.Set("Set-Cookie", hdr)
	}

	conn, err := websocket.Upgrade(w, r, upgradeHeader, 1024, 1024)
	if err != nil {
		libDatabox.Debug("[ERROR proxyWebSocket] Could not open websocket connection " + err.Error())
		http.Error(w, "Could not open websocket connection", http.StatusBadRequest)
	}
	defer connBackend.Close()

	errClient := make(chan error, 1)
	errBackend := make(chan error, 1)
	copy := func(from *websocket.Conn, to *websocket.Conn, errorChan chan error) {
		for {
			msgType, msg, err := from.ReadMessage()
			if err != nil {
				m := websocket.FormatCloseMessage(websocket.CloseNormalClosure, fmt.Sprintf("%v", err))
				if e, ok := err.(*websocket.CloseError); ok {
					if e.Code != websocket.CloseNoStatusReceived {
						m = websocket.FormatCloseMessage(e.Code, e.Text)
					}
				}
				errorChan <- err
				to.WriteMessage(websocket.CloseMessage, m)
				break
			}
			err = to.WriteMessage(msgType, msg)
			if err != nil {
				errorChan <- err
				break
			}
		}
	}

	libDatabox.Debug("[proxyWebSocket] copying data")
	go copy(conn, connBackend, errClient)
	go copy(connBackend, conn, errBackend)

	var message string
	select {
	case err = <-errClient:
		message = "Error when copying from backend to client: %v"
	case err = <-errBackend:
		message = "Error when copying from client to backend: %v"

	}
	if e, ok := err.(*websocket.CloseError); !ok || e.Code == websocket.CloseAbnormalClosure {
		libDatabox.Err("[ERROR proxyWebSocket]" + message + " " + err.Error())
	}

	return
}

func (d *DataboxProxyMiddleware) proxyHTTP(w http.ResponseWriter, r *http.Request, next http.Handler) {
	parts := strings.Split(r.URL.Path, "/")

	//lets proxy all ui request for now
	//proxy is bit of a hack for now but the proxy is moving to the core-network at some point soon
	/*if _, ok := d.proxyList[parts[1]]; ok == false {
		//no need to proxy
		next.ServeHTTP(w, r)
		return
	}*/
	if len(parts) < 3 || parts[2] != "ui" {
		//no need to proxy
		next.ServeHTTP(w, r)
		return
	}

	RequestURI := "https://" + parts[1] + ":8080/" + strings.Join(parts[2:], "/")

	if r.URL.RawQuery != "" {
		RequestURI = RequestURI + "?" + r.URL.RawQuery
	}

	//libDatabox.Debug("Proxying internal request to  " + RequestURI)

	var wg sync.WaitGroup

	copy := func() {
		req, err := http.NewRequest(r.Method, RequestURI, r.Body)
		for name, value := range r.Header {
			req.Header.Set(name, value[0])
		}
		resp, err := d.httpClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			wg.Done()
			return
		}

		for k, v := range resp.Header {
			w.Header().Set(k, v[0])
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()
		r.Body.Close()
		wg.Done()
	}

	wg.Add(1)
	go copy()
	wg.Wait()

	return
}
