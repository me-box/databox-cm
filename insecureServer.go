package main

import (
	"io/ioutil"
	"log"
	"net/http"

	"github.com/gorilla/mux"
)

var pubCertFullPath = "/certs/containerManagerPub.crt"

func ServeInsecure() {

	router := mux.NewRouter()
	static := http.FileServer(http.Dir("./www/http"))

	router.HandleFunc("/cert.pem", func(w http.ResponseWriter, r *http.Request) {
		pubCert, err := ioutil.ReadFile(pubCertFullPath)
		w.Header().Set("Content-Type", "application/x-pem-file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}
		w.Write(pubCert)
	}).Methods("GET")

	router.PathPrefix("/").Handler(static)

	log.Fatal(http.ListenAndServe(":80", router))
}
