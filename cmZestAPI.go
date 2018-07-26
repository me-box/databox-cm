package main

import (
	"encoding/json"
	"time"

	libDatabox "github.com/toshbrown/lib-go-databox"
)

func CmZestAPI(cm *ContainerManager) {

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
	ObserveResponseChan, err := cm.CmgrStoreClient.KVJSON.Observe("api")
	libDatabox.ChkErr(err)
	if err != nil {
		libDatabox.Err("Container Manager Zest API filed to register with store. " + err.Error())
	} else {
		for {
			select {
			case ObserveResponse := <-ObserveResponseChan:
				if ObserveResponse.Key == "install" {
					var sla libDatabox.SLA
					err := json.Unmarshal(ObserveResponse.Data, &sla)
					if err == nil {
						go cm.LaunchFromSLA(sla, false)
					} else {
						libDatabox.Err("Install command received invalid JSON " + err.Error())
					}
				}
				if ObserveResponse.Key == "restart" {
					libDatabox.Err("Not implemented")
				}
				if ObserveResponse.Key == "uninstall" {
					libDatabox.Err("Not implemented")
				}
			}
		}
	}

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
}

func populateDataSources(cm *ContainerManager) {
	//reset the list after restart
	cm.CmgrStoreClient.KVJSON.Write("data", "datasources", []byte("{}"))

	for {
		hyperCatRoot, err := cm.ArbiterClient.GetRootDataSourceCatalogue()
		if err != nil {
			libDatabox.Err("[populateDataSources] GetRootDataSourceCatalogue " + err.Error())
		}
		var datasources []libDatabox.HypercatItem
		for _, item := range hyperCatRoot.Items {
			//get the store cat
			storeURL, _ := libDatabox.GetStoreURLFromDsHref(item.Href)
			sc := libDatabox.NewCoreStoreClient(cm.ArbiterClient, "/run/secrets/ZMQ_PUBLIC_KEY", storeURL, false)
			storeCat, err := sc.GetStoreDataSourceCatalogue(item.Href)
			if err != nil {
				libDatabox.Err("[populateDataSources] Error GetStoreDataSourceCatalogue " + err.Error())
			}
			for _, ds := range storeCat.Items {
				libDatabox.Debug("[populateDataSources] " + ds.Href)
				datasources = append(datasources, ds)
			}
		}
		jsonString, err := json.Marshal(datasources)
		if err != nil {
			libDatabox.Err("[populateDataSources] Error " + err.Error())
		}
		cm.CmgrStoreClient.KVJSON.Write("data", "datasources", jsonString)

		//sleep for a bit
		time.Sleep(time.Second * 15)
	}
}
