package main

import (
	"encoding/json"
	"io/ioutil"
	"os"

	libDatabox "github.com/me-box/lib-go-databox"
)

func main() {

	DOCKER_API_VERSION := "1.37" //TODO store version in ContainerManagerOptions
	os.Setenv("DOCKER_API_VERSION", DOCKER_API_VERSION)

	//get cm options from secret DATABOX_CM_OPTIONS
	cmOptionsJSON, err := ioutil.ReadFile("/run/secrets/DATABOX_CM_OPTIONS")
	libDatabox.ChkErrFatal(err)
	var options libDatabox.ContainerManagerOptions
	err = json.Unmarshal(cmOptionsJSON, &options)
	libDatabox.ChkErrFatal(err)

	generateDataboxCertificates(options.InternalIPs, options.ExternalIP, options.Hostname)
	generateArbiterTokens()

	databox := NewDataboxLoader(&options)
	rootCASecretID, zmqPublic, zmqPrivate := databox.Start()
	libDatabox.Debug("key IDs :: " + rootCASecretID + " " + zmqPublic + " " + zmqPrivate)

	cm := NewContainerManager(rootCASecretID, zmqPublic, zmqPrivate, &options)
	_, err = cm.WaitForService("arbiter", 10)
	libDatabox.ChkErrFatal(err)

	//Start the databox cm Uis and do initial configuration
	cm.Start()

	//Wait for a quit message
	quit := make(chan int)
	<-quit // blocks until quit is written to. Which is never for now!!
}

var certsBasePath = "./certs"

func generateArbiterTokens() {
	components := []string{
		"container-manager",
		"databox-network",
		"arbiter",
	}

	if _, err := os.Stat(certsBasePath); err != nil {
		os.Mkdir(certsBasePath, 0700)
	}

	for _, name := range components {
		if _, err := os.Stat(certsBasePath + "/arbiterToken-" + name); err == nil {
			continue
		}
		GenerateArbiterTokenToFile(certsBasePath + "/arbiterToken-" + name)
	}
}

func generateDataboxCertificates(IPs []string, externalIP string, hostname string) {

	rootCAPath := certsBasePath + "/containerManager.crt"
	rootCAPathPub := certsBasePath + "/containerManagerPub.crt"

	if _, err := os.Stat(rootCAPath); err != nil {
		GenRootCA(rootCAPath, rootCAPathPub)
	}

	//container-manager needs extra information
	if _, err := os.Stat(certsBasePath + "/container-manager.pem"); err != nil {
		libDatabox.Debug("[generateDataboxCertificates] making cert for container-manager")
		GenCertToFile(
			rootCAPath,
			"container-manager",
			append([]string{externalIP, "127.0.0.1"}, IPs...), //“…” is syntax for variadic arguments
			[]string{"container-manager", "localhost", hostname},
			certsBasePath+"/container-manager.pem",
		)
	}

	components := []string{
		"databox-network",
		"export-service",
		"arbiter",
		"app-server",
	}

	for _, name := range components {
		if _, err := os.Stat(certsBasePath + "/" + name + ".pem"); err == nil {
			continue
		}
		libDatabox.Debug("[generateDataboxCertificates] making cert for " + name)
		libDatabox.Info("Making cert " + certsBasePath + "/" + name + ".pem")
		GenCertToFile(
			rootCAPath,
			name,
			[]string{"127.0.0.1"},
			[]string{name, "localhost"},
			certsBasePath+"/"+name+".pem",
		)
	}

}
