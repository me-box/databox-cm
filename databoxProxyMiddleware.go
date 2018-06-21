package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	libDatabox "github.com/toshbrown/lib-go-databox"
)

type DataboxProxyMiddleware struct {
	mutex      sync.Mutex
	proxyList  map[string]string
	httpClient *http.Client
	next       http.Handler
}

func NewProxyMiddleware(rootCertPath string) *DataboxProxyMiddleware {

	var h *http.Client
	if rootCertPath != "" {
		h = libDatabox.NewDataboxHTTPsAPIWithPaths(rootCertPath)
	}

	d := &DataboxProxyMiddleware{
		mutex:      sync.Mutex{},
		httpClient: h,
		proxyList:  make(map[string]string),
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

	parts := strings.Split(r.URL.Path, "/")

	if _, ok := d.proxyList[parts[1]]; ok == false {
		//no need to proxy
		next.ServeHTTP(w, r)
		return
	}

	backendURL := "wss://" + parts[1] + ":8080/" + strings.Join(parts[2:], "/")

	dialer := &websocket.Dialer{}

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
		libDatabox.Err(message + " " + err.Error())
	}

	return
}

func (d *DataboxProxyMiddleware) proxyHTTP(w http.ResponseWriter, r *http.Request, next http.Handler) {
	parts := strings.Split(r.URL.Path, "/")

	if _, ok := d.proxyList[parts[1]]; ok == false {
		//no need to proxy
		next.ServeHTTP(w, r)
		return
	}

	RequestURI := "https://" + parts[1] + ":8080/" + strings.Join(parts[2:], "/")

	if r.URL.RawQuery != "" {
		RequestURI = RequestURI + "?" + r.URL.RawQuery
	}

	libDatabox.Debug("Proxying internal request to  " + RequestURI)

	var wg sync.WaitGroup

	copy := func() {
		defer wg.Done()
		req, err := http.NewRequest(r.Method, RequestURI, r.Body)
		for name, value := range r.Header {
			req.Header.Set(name, value[0])
		}
		resp, err := d.httpClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		defer resp.Body.Close()
		defer r.Body.Close()

		for k, v := range resp.Header {
			w.Header().Set(k, v[0])
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)

	}

	wg.Add(1)
	go copy()
	wg.Wait()

	return
}

func (d *DataboxProxyMiddleware) Add(containerName string) {
	//d.mutex.Lock()
	//defer d.mutex.Unlock()
	d.proxyList[containerName] = containerName
	return
}

func (d *DataboxProxyMiddleware) Del(containerName string) {
	//d.mutex.Lock()
	//defer d.mutex.Unlock()
	_, ok := d.proxyList[containerName]
	if ok {
		delete(d.proxyList, containerName)
	}
	return
}

func (d *DataboxProxyMiddleware) Exists(containerName string) bool {
	//d.mutex.Lock()
	//defer d.mutex.Unlock()
	_, ok := d.proxyList[containerName]
	return ok
}
