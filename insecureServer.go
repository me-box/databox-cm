package main

import (
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	libDatabox "github.com/me-box/lib-go-databox"
)

var pubCertFullPath = "/certs/containerManagerPub.crt"

func ServeInsecure() {

	router := mux.NewRouter()
	static := http.FileServer(http.Dir("./www/http"))
	databoxHttpsClient := libDatabox.NewDataboxHTTPsAPIWithPaths("/certs/containerManager.crt")

	router.PathPrefix("/cert{.pem|.der}").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		CertProxy(w, r, databoxHttpsClient)
	})
	router.PathPrefix("/").Handler(static)

	log.Fatal(http.ListenAndServe(":80", router))
}

func CertProxy(w http.ResponseWriter, r *http.Request, databoxHttpsClient *http.Client) {

	RequestURI := "https://core-ui:8080/ui/" + r.URL.Path

	libDatabox.Debug("InsecureServer proxing from: " + r.URL.RequestURI() + " to: " + RequestURI)
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
