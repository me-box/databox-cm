package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	libDatabox "github.com/me-box/lib-go-databox"
	qrcode "github.com/skip2/go-qrcode"
)

//data required for an install request
type installRequest struct {
	Manifest libDatabox.Manifest `json:"manifest"`
}

type restartRequest struct {
	Name string `json:"name"`
}

type uninstallRequest struct {
	Name string `json:"name"`
}

func CmZestAPI(cm *ContainerManager) {

	//expose functions
	cm.CmgrStoreClient.FUNC.Register("databox", "ServiceStatus", libDatabox.ContentTypeJSON, ServiceStatus(cm))

	//
	//Register and observe API command endpoints
	//
	APImetadata := libDatabox.DataSourceMetadata{
		Description:    "Databox container manager API",
		ContentType:    "application/json",
		Vendor:         "Databox",
		DataSourceType: "databox:container-manager:api",
		DataSourceID:   "api",
		StoreType:      "kv",
		IsActuator:     true,
		Unit:           "",
		Location:       "",
	}
	cm.CmgrStoreClient.RegisterDatasource(APImetadata)
	go processAPICommands(cm)

	//
	// Register Data endpoints and start goroutines to updated them
	// this is the only way to expose data as we do not have an RPC like api
	// see https://github.com/me-box/databox/issues/273
	//
	// For now this will work but its less then optimal
	//
	DataMetadata := libDatabox.DataSourceMetadata{
		Description:    "Databox container manager data API",
		ContentType:    "application/json",
		Vendor:         "Databox",
		DataSourceType: "databox:container-manager:data",
		DataSourceID:   "data",
		StoreType:      "kv",
		IsActuator:     true,
		Unit:           "",
		Location:       "",
	}
	cm.CmgrStoreClient.RegisterDatasource(DataMetadata)
	go populateDataSources(cm)
	go populateServiceStatus(cm)
	go populateMobileAppQrCodeAndCerts(cm)

}

func ServiceStatus(cm *ContainerManager) libDatabox.FuncHandler {
	libDatabox.Info("API: registering ServiceStatus")
	return func(contnetType libDatabox.StoreContentType, payload []byte) []byte {
		libDatabox.Info("API: ServiceStatus called contentType=" + string(contnetType) + "Payload=" + string(payload))
		type listResult struct {
			Name         string          `json:"name"`
			Type         string          `json:"type"`
			DesiredState swarm.TaskState `json:"desiredState"`
			State        swarm.TaskState `json:"state"`
			Status       swarm.TaskState `json:"status"`
		}

		services, _ := cm.cli.ServiceList(context.Background(), types.ServiceListOptions{})
		res := []listResult{}
		for _, service := range services {

			_, exists := service.Spec.Labels["databox.type"]
			if exists == false {
				//its not a databox service
				continue
			}

			lr := listResult{
				Name: service.Spec.Name,
				Type: service.Spec.Labels["databox.type"],
			}

			taskFilters := filters.NewArgs()
			taskFilters.Add("service", service.Spec.Name)
			tasks, _ := cm.cli.TaskList(context.Background(), types.TaskListOptions{
				Filters: taskFilters,
			})
			if len(tasks) > 0 {
				latestTasks := tasks[0]
				latestTime := latestTasks.UpdatedAt

				for _, t := range tasks {
					if t.UpdatedAt.After(latestTime) {
						latestTasks = t
						latestTime = latestTasks.UpdatedAt
					}
				}

				lr.DesiredState = latestTasks.DesiredState
				lr.State = latestTasks.Status.State
				lr.Status = latestTasks.Status.State
			}

			res = append(res, lr)
		}

		jsonString, err := json.Marshal(res)
		if err != nil {
			libDatabox.Err("[ServiceStatus] Error " + err.Error())
		}

		libDatabox.Info("API: ServiceStatus done returning=" + string(jsonString))
		return jsonString
	}
}

func processAPICommands(cm *ContainerManager) {
	ObserveResponseChan, err := cm.CmgrStoreClient.KVJSON.Observe("api")
	libDatabox.ChkErr(err)
	if err != nil {
		libDatabox.Err("Container Manager Zest API filed to register with store. " + err.Error())
	} else {
		for {
			select {
			case ObserveResponse := <-ObserveResponseChan:
				if ObserveResponse.Key == "install" {
					var installData installRequest
					err := json.Unmarshal(ObserveResponse.Data, &installData)
					if err == nil {
						sla := convertManifestToSLA(installData)
						go cm.LaunchFromSLA(sla, true)
					} else {
						libDatabox.Err("Install command received invalid JSON " + err.Error())
					}
				}
				if ObserveResponse.Key == "restart" {
					var request restartRequest
					err := json.Unmarshal(ObserveResponse.Data, &request)
					libDatabox.ChkErr(err)
					libDatabox.Debug("ObserveResponse data = " + string(ObserveResponse.Data))
					libDatabox.Debug("request.Name data = " + request.Name)
					if err == nil && request.Name != "" {
						go cm.Restart(request.Name)
					} else if err == nil {
						libDatabox.Err("Restart command received invalid JSON request.name is blank")
					} else {
						libDatabox.Err("Restart command received invalid JSON " + err.Error())
					}
				}
				if ObserveResponse.Key == "uninstall" {
					var request uninstallRequest
					err := json.Unmarshal(ObserveResponse.Data, &request)
					libDatabox.ChkErr(err)
					libDatabox.Debug("ObserveResponse data = " + string(ObserveResponse.Data))
					libDatabox.Debug("request.Name data = " + request.Name)
					if err == nil && request.Name != "" {
						go cm.Uninstall(request.Name)
					} else if err == nil {
						libDatabox.Err("Uninstall command received invalid JSON request.name is blank")
					} else {
						libDatabox.Err("Uninstall command received invalid JSON " + err.Error())
					}
				}
			}
		}
	}
}

func convertManifestToSLA(ir installRequest) libDatabox.SLA {

	sla := libDatabox.SLA{
		Name:                 ir.Manifest.Name,
		DataboxType:          ir.Manifest.DataboxType,
		Repository:           ir.Manifest.Repository,
		ExportWhitelists:     ir.Manifest.ExportWhitelists,
		ExternalWhitelist:    ir.Manifest.ExternalWhitelist,
		ResourceRequirements: ir.Manifest.ResourceRequirements,
		DisplayName:          ir.Manifest.DisplayName,
		StoreURL:             ir.Manifest.StoreURL,
		//Registry:             ir.Manifest., TODO is this needed??
		Datasources: ir.Manifest.DataSources,
	}
	return sla
}

func populateMobileAppQrCodeAndCerts(cm *ContainerManager) {

	//Make the public key available
	var pubCertFullPath = "/certs/containerManagerPub.crt"
	pubCert, err := ioutil.ReadFile(pubCertFullPath)
	libDatabox.ChkErr(err)
	if err == nil {
		err := cm.CmgrStoreClient.KVBin.Write("data", "cert.pem", pubCert)
		libDatabox.ChkErr(err)
	}

	var pubCertFullPathDer = "/certs/containerManagerPub.der"
	pubCertDer, err := ioutil.ReadFile(pubCertFullPathDer)
	libDatabox.ChkErr(err)
	if err == nil {
		err := cm.CmgrStoreClient.KVBin.Write("data", "cert.der", pubCertDer)
		libDatabox.ChkErr(err)
	}

	//make the config qr-code  available
	type qrData struct {
		IP         string   `json:"ip"`
		IPs        []string `json:"ips"`
		IPExternal string   `json:"ipExternal"`
		Hostname   string   `json:hostname`
		Token      string   `json:"token"`
	}

	password, err := cm.Store.LoadPassword()
	libDatabox.ChkErr(err)

	data := qrData{
		IP:         cm.Options.InternalIPs[0],
		IPs:        cm.Options.InternalIPs,
		IPExternal: cm.Options.ExternalIP,
		Hostname:   cm.Options.Hostname,
		Token:      "Token=" + password,
	}

	json, err := json.Marshal(data)
	if err != nil {
		libDatabox.Err("[/qrcode.png] Error parsing JSON " + err.Error())
		return
	}
	var png []byte
	png, err = qrcode.Encode(string(json), qrcode.Medium, 256)
	if err != nil {
		libDatabox.Err("[/api/qrcode.png] Error making  qrcode" + err.Error())
		return
	}

	err = cm.CmgrStoreClient.KVBin.Write("data", "qrcode.png", png)
	libDatabox.ChkErr(err)
}

func populateServiceStatus(cm *ContainerManager) {

	//reset the list after restart
	cm.CmgrStoreClient.KVJSON.Write("data", "containerStatus", []byte("{}"))

	type listResult struct {
		Name         string          `json:"name"`
		Type         string          `json:"type"`
		DesiredState swarm.TaskState `json:"desiredState"`
		State        swarm.TaskState `json:"state"`
		Status       swarm.TaskState `json:"status"`
	}

	for {
		//libDatabox.Debug("[populateServiceStatus] Updating ...")
		services, _ := cm.cli.ServiceList(context.Background(), types.ServiceListOptions{})
		res := []listResult{}
		for _, service := range services {

			_, exists := service.Spec.Labels["databox.type"]
			if exists == false {
				//its not a databox service
				continue
			}

			lr := listResult{
				Name: service.Spec.Name,
				Type: service.Spec.Labels["databox.type"],
			}

			taskFilters := filters.NewArgs()
			taskFilters.Add("service", service.Spec.Name)
			tasks, _ := cm.cli.TaskList(context.Background(), types.TaskListOptions{
				Filters: taskFilters,
			})
			if len(tasks) > 0 {
				latestTasks := tasks[0]
				latestTime := latestTasks.UpdatedAt

				for _, t := range tasks {
					if t.UpdatedAt.After(latestTime) {
						latestTasks = t
						latestTime = latestTasks.UpdatedAt
					}
				}

				lr.DesiredState = latestTasks.DesiredState
				lr.State = latestTasks.Status.State
				lr.Status = latestTasks.Status.State
			}

			res = append(res, lr)
		}

		jsonString, err := json.Marshal(res)
		if err != nil {
			libDatabox.Err("[populateDataSources] Error " + err.Error())
		}
		err = cm.CmgrStoreClient.KVJSON.Write("data", "containerStatus", jsonString)
		if err != nil {
			libDatabox.Err("[populateDataSources] Error " + err.Error())
		}
		//sleep for a bit
		time.Sleep(time.Second * 1)
	}
}

func populateDataSources(cm *ContainerManager) {
	//reset the list after restart
	cm.CmgrStoreClient.KVJSON.Write("data", "dataSources", []byte("{}"))

	for {
		//libDatabox.Debug("[populateDataSources] Updating ...")
		hyperCatRoot, err := cm.ArbiterClient.GetRootDataSourceCatalogue()
		if err != nil {
			libDatabox.Err("[populateDataSources] GetRootDataSourceCatalogue " + err.Error())
		}
		var datasources []libDatabox.HypercatItem
		for _, item := range hyperCatRoot.Items {
			if strings.Contains(item.Href, "-core-store:5555") == false {
				//TODO Tell john this should not be here !!
				continue
			}
			//get the store cat
			storeURL, _ := libDatabox.GetStoreURLFromDsHref(item.Href)
			sc := libDatabox.NewCoreStoreClient(cm.ArbiterClient, "/run/secrets/ZMQ_PUBLIC_KEY", storeURL, false)
			storeCat, err := sc.GetStoreDataSourceCatalogue(item.Href)
			if err != nil {
				libDatabox.Warn("[populateDataSources] GetStoreDataSourceCatalogue " + item.Href + " " + err.Error())
			}
			for _, ds := range storeCat.Items {
				datasources = append(datasources, ds)
			}
		}
		jsonString, err := json.Marshal(datasources)
		if err != nil {
			libDatabox.Err("[populateDataSources] Error " + err.Error())
		}
		err = cm.CmgrStoreClient.KVJSON.Write("data", "dataSources", jsonString)
		if err != nil {
			libDatabox.Err("[populateDataSources] Error " + err.Error())
		}
		//sleep for a bit
		time.Sleep(time.Second * 15)
	}
}
