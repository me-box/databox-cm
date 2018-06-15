package main

import (
	"io"
	"net/http"
	"strings"
	"sync"

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

		parts := strings.Split(r.URL.Path, "/")

		//d.mutex.Lock()
		//defer d.mutex.Unlock()
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
		return
	}

	return http.HandlerFunc(proxy)
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
