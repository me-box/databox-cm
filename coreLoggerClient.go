package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"

	libDatabox "github.com/me-box/lib-go-databox"
)

type CoreLoggerClient struct {
}

type startLoggingRequest struct {
	path            string
	key             string
	main_endpoint   string
	router_endpoint string
	max_age         string
	token           string
}

func StartLoggingArbiter(observeServerHost string, observeServerHostRouterPort string, path string, zmqPublicKeyPath string, ArbiterKey string, max_age string, request *http.Client, arbiterClient *libDatabox.ArbiterClient) error {
	libDatabox.Info("Setting up core-logger")

	//get the server key
	serverKey, err := ioutil.ReadFile(zmqPublicKeyPath)
	if err != nil {
		fmt.Println("Warning:: failed to read ZMQ_PUBLIC_KEY using default value")
		serverKey = []byte("vl6wu0A@XP?}Or/&BR#LSxn>A+}L)p44/W[wXL3<")
	}

	data := fmt.Sprintf(`[{"path": "%s"}, {"key": "%s"}, {"main_endpoint": "%s"}, {"router_endpoint": "%s"}, {"max_age": %s}, {"token": "%s"}]`, path, serverKey, observeServerHost, observeServerHostRouterPort, max_age, ArbiterKey)

	libDatabox.Info(data)
	err = put("startLogging", request, []byte(data), "https://core-logger:8080/observe/arbiter")

	return err

}

func StartLoggingStore(observeServerHost string, observeServerHostRouterPort string, path string, zmqPublicKeyPath string, max_age string, request *http.Client, arbiterClient *libDatabox.ArbiterClient) error {
	libDatabox.Info("Setting up core-logger")

	token, err := arbiterClient.RequestDeligatedToken("core-logger", observeServerHost+path, "GET", "")
	if err != nil {
		return err
	}

	u, err := url.Parse(observeServerHost)
	if err != nil {
		return err
	}

	targetHost, _, err1 := net.SplitHostPort(u.Host)
	if err != nil {
		return err1
	}

	//get the server key
	serverKey, err := ioutil.ReadFile(zmqPublicKeyPath)
	if err != nil {
		fmt.Println("Warning:: failed to read ZMQ_PUBLIC_KEY using default value")
		serverKey = []byte("vl6wu0A@XP?}Or/&BR#LSxn>A+}L)p44/W[wXL3<")
	}

	data := fmt.Sprintf(`[{"path": "%s"}, {"key": "%s"}, {"main_endpoint": "%s"}, {"router_endpoint": "%s"}, {"max_age": %s}, {"token": "%s"}]`, path, serverKey, observeServerHost, observeServerHostRouterPort, max_age, token)

	libDatabox.Info(data)
	err = put("startLogging", request, []byte(data), "https://core-logger:8080/observe/"+targetHost)

	return err

}

func put(LogFnName string, request *http.Client, data []byte, URL string) error {

	libDatabox.Debug("[CoreLoggerClient " + LogFnName + "] PUT JSON :: " + string(data))

	req, err := http.NewRequest("PUT", URL, bytes.NewBuffer(data))
	if err != nil {
		libDatabox.Err("[" + LogFnName + "] Error:: " + err.Error())
		return err
	}
	//req.Header.Set("x-api-key", cnc.CM_KEY)
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	resp, err := request.Do(req)

	if err != nil {
		libDatabox.Err("[" + LogFnName + "] Error " + err.Error())
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		response, _ := ioutil.ReadAll(resp.Body)
		libDatabox.Err("[" + LogFnName + "] PostError StatusCode=" + strconv.Itoa(resp.StatusCode) + " data=" + string(data) + "response=" + string(response))
		return err
	}

	return nil
}

func (clc CoreLoggerClient) requestDelegatedArbiterToken(forHost string) {

}
