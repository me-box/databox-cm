package main

import (
	"crypto/rand"
	b64 "encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	libDatabox "github.com/me-box/lib-go-databox"
)

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

type DataboxAuthMiddleware struct {
	sync.Mutex
	session  string //TODO this should be per user or at least per device
	proxy    *DataboxProxyMiddleware
	password string
	next     http.Handler
}

func NewAuthMiddleware(password string, proxy *DataboxProxyMiddleware) *DataboxAuthMiddleware {

	return &DataboxAuthMiddleware{
		password: password,
		session:  "",
		proxy:    proxy,
	}
}

func (d *DataboxAuthMiddleware) AuthMiddleware(next http.Handler) http.Handler {

	auth := func(w http.ResponseWriter, r *http.Request) {

		//libDatabox.Debug("AuthMiddleware path=" + r.URL.Path)

		parts := strings.Split(r.URL.Path, "/")

		if _, ok := allowedStaticPaths[parts[1]]; ok {
			//its allowed no auth needed
			//libDatabox.Debug("its allowed no auth needed")
			next.ServeHTTP(w, r)
			return
		}

		if _, ok := allowedStaticPaths[r.URL.Path]; ok {
			//its allowed no auth needed
			//libDatabox.Debug("its allowed no auth needed")
			next.ServeHTTP(w, r)
			return
		}

		if _, ok := allowedStaticPaths[filepath.Dir(r.URL.Path)]; ok {
			//its allowed no auth needed
			libDatabox.Debug("its path allowed no auth needed")
			next.ServeHTTP(w, r)
			return
		}

		d.Lock()
		defer d.Unlock()
		//handle connect request
		if len(parts) >= 3 && parts[2] == "connect" {
			//check password and issue session token if its OK
			libDatabox.Debug("Connect called checking password")
			if ("Token " + d.password) == r.Header.Get("Authorization") {
				libDatabox.Debug("Password OK!")
				if d.session == "" {
					//make a new session token
					b := make([]byte, 24)
					rand.Read(b) //TODO This could error should check
					d.session = b64.StdEncoding.EncodeToString(b)
				}

				cookie := http.Cookie{
					Name:   "session",
					Value:  d.session,
					Domain: r.URL.Hostname(),
					Path:   "/",
				}
				http.SetCookie(w, &cookie)
				fmt.Fprintf(w, "connected")
				return
			}

			libDatabox.Err("Password validation error!" + r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, "Authorization Required")
			return
		}

		//Its not a connect request
		// we must have a valid session cookie
		sessionCookie, _ := r.Cookie("session")
		if sessionCookie != nil && d.session == sessionCookie.Value {
			//libDatabox.Debug("Session cookie OK")
			//session cookie is ok continue to the next Middleware
			next.ServeHTTP(w, r)
			return
		}

		//if we get here were unauthorised
		libDatabox.Warn("Authorization failed")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, "Authorization Required")
		return
	}
	return http.HandlerFunc(auth)
}
