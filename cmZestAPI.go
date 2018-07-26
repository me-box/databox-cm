package main

import (
	"encoding/json"

	libDatabox "github.com/toshbrown/lib-go-databox"
)

func CmZestAPI(cm *ContainerManager) {

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

	storeClient := cm.CoreStoreClient

	storeClient.RegisterDatasource(APImetadata)

	ObserveResponseChan, err := storeClient.KVJSON.Observe("api")
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
						libDatabox.Err("Install comand received invalid JSON " + err.Error())
					}
				}
				if ObserveResponse.Key == "listDataSources" {
					libDatabox.Err("Not implemented")
				}
			}
		}
	}
}
