package main

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	libDatabox "github.com/toshbrown/lib-go-databox"
)

var dboxproxy *DataboxProxyMiddleware

func ServeSecure(cm *ContainerManager, password string) {

	//pull required databox components from the ContainerManager
	//cli := cm.cli
	//ac := cm.ArbiterClient

	//start the https server for the app UI
	r := mux.NewRouter()

	dboxproxy = NewProxyMiddleware("/certs/containerManager.crt")
	databoxHttpsClient := libDatabox.NewDataboxHTTPsAPIWithPaths("/certs/containerManager.crt")

	dboxauth := NewAuthMiddleware(password, dboxproxy)

	libDatabox.Debug("Installing databox Middlewares")
	r.Use(dboxauth.AuthMiddleware, dboxproxy.ProxyMiddleware)

	//Proxy to the core-ui-app
	r.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy(w, r, databoxHttpsClient)
	})

	libDatabox.ChkErrFatal(http.ListenAndServeTLS(":443", "./certs/container-manager.pem", "./certs/container-manager.pem", r))
}

func proxy(w http.ResponseWriter, r *http.Request, databoxHttpsClient *http.Client) {
	parts := strings.Split(r.URL.Path, "/")
	var RequestURI string
	if len(parts) < 2 {
		RequestURI = "https://core-ui:8080/ui/"
	} else {
		RequestURI = "https://core-ui:8080/ui/" + strings.Join(parts[2:], "/")
	}
	libDatabox.Debug("SecureServer proxing from: " + r.URL.RequestURI() + " to: " + RequestURI)
	var wg sync.WaitGroup
	copy := func() {
		defer wg.Done()
		req, err := http.NewRequest(r.Method, RequestURI, r.Body)
		for name, value := range r.Header {
			req.Header.Set(name, value[0])
		}
		resp, err := databoxHttpsClient.Do(req)
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

}
