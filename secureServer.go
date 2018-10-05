package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	libDatabox "github.com/me-box/lib-go-databox"
)

func ServeSecure(cm *ContainerManager, password string) {

	databoxHttpsClient := libDatabox.NewDataboxHTTPsAPIWithPaths("/certs/containerManager.crt")
	CM_HTTPS_CA_ROOT_CERT, _ := ioutil.ReadFile("/certs/containerManager.crt")
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(CM_HTTPS_CA_ROOT_CERT))

	//setup the session token map
	sessionToken = make(map[string]int)

	//Proxy
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		//Auth
		if auth(w, r, password) {
			if websocket.IsWebSocketUpgrade(r) {
				webSocketProxy(w, r, roots, cm)
			} else {
				proxy(w, r, databoxHttpsClient, cm)
			}
		}

	})

	libDatabox.ChkErrFatal(http.ListenAndServeTLS(":443", "./certs/container-manager.pem", "./certs/container-manager.pem", nil))
}

var allowedStaticPaths = map[string]string{
	"css":                  "",
	"js":                   "",
	"icons":                "",
	"img":                  "",
	"":                     "",
	"cordova.js":           "",
	"/core-ui/ui":          "",
	"/core-ui/ui/js":       "",
	"/core-ui/ui/css":      "",
	"/core-ui/ui/img":      "",
	"/core-ui/ui/cert.pem": "",
	"/ui/cert.pem":         "",
	"/cert.pem":            "",
}

//A map to hold session tokens
var sessionToken map[string]int

func auth(w http.ResponseWriter, r *http.Request, password string) bool {

	parts := strings.Split(r.URL.Path, "/")

	if _, ok := allowedStaticPaths[parts[1]]; ok {
		//its allowed no auth needed
		return true
	}

	if _, ok := allowedStaticPaths[r.URL.Path]; ok {
		//its allowed no auth needed
		return true
	}

	if _, ok := allowedStaticPaths[filepath.Dir(r.URL.Path)]; ok {
		//its allowed no auth needed
		return true
	}

	if ("Token " + password) == r.Header.Get("Authorization") {
		libDatabox.Debug("Password OK!")
		//make a new session token
		b := make([]byte, 24)
		rand.Read(b) //TODO This could error should check
		token := b64.StdEncoding.EncodeToString(b)
		sessionToken[token] = 1

		cookie := http.Cookie{
			Name:   "session",
			Value:  token,
			Domain: r.URL.Hostname(),
			Path:   "/",
		}
		http.SetCookie(w, &cookie)
		fmt.Fprintf(w, "connected")
		return false
	}

	sessionCookie, _ := r.Cookie("session")
	if sessionCookie != nil && sessionToken[sessionCookie.Value] == 1 {
		//libDatabox.Debug("Session cookie OK")
		return true
	}

	libDatabox.Err("Password validation error!" + r.Header.Get("Authorization"))
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, "Authorization Required")
	return false
}

func webSocketProxy(w http.ResponseWriter, r *http.Request, roots *x509.CertPool, cm *ContainerManager) {

	libDatabox.Debug("[proxyWebSocket] started")

	parts := strings.Split(r.URL.Path, "/")

	backendURL := "wss://" + parts[1] + ":8080/" + strings.Join(parts[2:], "/")

	//libDatabox.Debug("[proxyWebSocket] proxing to " + backendURL)

	if !cm.IsInstalled(parts[1]) {
		//we have no host this is probably a malformed path (error in app or driver html)
		libDatabox.Debug("[HTTP proxy] return 404 for websocket")
		http.Error(w, "Page not found", http.StatusNotFound)
		return
	}

	dialer := &websocket.Dialer{
		TLSClientConfig: &tls.Config{
			RootCAs: roots,
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

func proxy(w http.ResponseWriter, r *http.Request, databoxHttpsClient *http.Client, cm *ContainerManager) {
	parts := strings.Split(r.URL.Path, "/")
	var RequestURI string
	if len(parts) < 3 {
		RequestURI = "https://core-ui:8080/ui/"
	} else {
		RequestURI = "https://" + parts[1] + ":8080/" + strings.Join(parts[2:], "/")
		if !cm.IsInstalled(parts[1]) {
			//we have no host this is probably a malformed path (error in app or driver html)
			libDatabox.Debug("[HTTP proxy] return 404 for  " + RequestURI)
			http.Error(w, "Page not found", http.StatusNotFound)
			return
		}
	}
	var wg sync.WaitGroup
	copy := func() {
		defer wg.Done()
		req, err := http.NewRequest(r.Method, RequestURI, r.Body)
		for name, value := range r.Header {
			req.Header.Set(name, value[0])
		}
		resp, err := databoxHttpsClient.Do(req)
		if resp != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			libDatabox.Err("[HTTP proxy] got error " + err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for k, v := range resp.Header {
			w.Header().Set(k, v[0])
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)

	}

	wg.Add(1)
	go copy()
	wg.Wait()

}
