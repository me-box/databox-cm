package main

import (
	"context"

	libDatabox "github.com/me-box/lib-go-databox"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/swarm"
)

//This contains the functions used to start the databoxes core services like the app-store driver and core-logger

// Start the core-logger as an app
func (cm ContainerManager) startCoreLogger() {
	libDatabox.Info("Installing core-logger")

	name := "core-logger"

	sla := libDatabox.SLA{
		Name:        name,
		DockerImage: cm.Options.DefaultRegistry + "/core-logger-" + cm.Options.Arch + ":" + cm.Options.Version,
		DataboxType: libDatabox.DataboxTypeApp,
		Datasources: []libDatabox.DataSource{
			libDatabox.DataSource{
				Type:          "databox:container-manager:data",
				Required:      true,
				Name:          "container-manager:data",
				Clientid:      "CM_DATA",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPairBool{
							Rel: "urn:X-databox:rels:isActuator",
							Val: false,
						},
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "data",
						},
					},
					Href: "tcp://container-manager-" + cm.CoreStoreName + ":5555/kv/data",
				},
			},
		},
	}

	err := cm.LaunchFromSLA(sla, false)
	libDatabox.ChkErr(err)

	_, err = cm.WaitForService(name, 10)
	libDatabox.ChkErr(err)

}

// launchAppStore start the app store driver
func (cm ContainerManager) launchAppStore() {
	name := cm.AppStoreName

	sla := libDatabox.SLA{
		Name:        name,
		DockerImage: cm.Options.AppServerImage,
		DataboxType: libDatabox.DataboxTypeDriver,
		ResourceRequirements: libDatabox.ResourceRequirements{
			Store: cm.CoreStoreName,
		},
		ExternalWhitelist: []libDatabox.ExternalWhitelist{
			libDatabox.ExternalWhitelist{
				Urls:        []string{"https://github.com", "https://www.github.com"},
				Description: "Needed to access the manifests stored on github",
			},
		},
	}

	err := cm.LaunchFromSLA(sla, false)
	libDatabox.ChkErr(err)

	_, err = cm.WaitForService(name, 10)
	libDatabox.ChkErr(err)

}

func (cm ContainerManager) startExportService() {

	service := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": "system"},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:   cm.Options.ExportServiceImage,
				Env:     []string{"DATABOX_ARBITER_ENDPOINT=tcp://arbiter:4444"},
				Secrets: cm.genorateSecrets("export-service", libDatabox.DataboxTypeStore),
			},
			Networks: []swarm.NetworkAttachmentConfig{swarm.NetworkAttachmentConfig{
				Target: "databox-system-net",
			}},
		},
	}

	service.Name = "export-service"

	serviceOptions := types.ServiceCreateOptions{}

	pullImageIfRequired(service.TaskTemplate.ContainerSpec.Image, cm.Options.DefaultRegistry, cm.Options.DefaultRegistryHost)

	_, err := cm.cli.ServiceCreate(context.Background(), service, serviceOptions)
	libDatabox.ChkErrFatal(err)

}

// launchAppStore start the app store driver
func (cm ContainerManager) launchUI() {

	name := cm.CoreIUName

	sla := libDatabox.SLA{
		Name:        name,
		DockerImage: cm.Options.CoreUIImage,
		DataboxType: libDatabox.DataboxTypeApp,
		Datasources: []libDatabox.DataSource{
			libDatabox.DataSource{
				Type:          "databox:func:ServiceStatus",
				Required:      true,
				Name:          "ServiceStatus",
				Clientid:      "CM_API_ServiceStatus",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPairBool{
							Rel: "urn:X-databox:rels:isFunc",
							Val: true,
						},
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "ServiceStatus",
						},
					},
					Href: "tcp://container-manager-" + cm.CoreStoreName + ":5555/",
				},
			},
			libDatabox.DataSource{
				Type:          "databox:func:ListAllDatasources",
				Required:      true,
				Name:          "ListAllDatasources",
				Clientid:      "CM_API_ListAllDatasources",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPairBool{
							Rel: "urn:X-databox:rels:isFunc",
							Val: true,
						},
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "ListAllDatasources",
						},
					},
					Href: "tcp://container-manager-" + cm.CoreStoreName + ":5555/",
				},
			},
			libDatabox.DataSource{
				Type:          "databox:container-manager:api",
				Required:      true,
				Name:          "container-manager:api",
				Clientid:      "CM_API",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPairBool{
							Rel: "urn:X-databox:rels:isActuator",
							Val: true,
						},
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "api",
						},
					},
					Href: "tcp://container-manager-" + cm.CoreStoreName + ":5555/kv/api",
				},
			},
			libDatabox.DataSource{
				Type:          "databox:container-manager:data",
				Required:      true,
				Name:          "container-manager:data",
				Clientid:      "CM_DATA",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPairBool{
							Rel: "urn:X-databox:rels:isActuator",
							Val: false,
						},
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "data",
						},
					},
					Href: "tcp://container-manager-" + cm.CoreStoreName + ":5555/kv/data",
				},
			},
			libDatabox.DataSource{
				Type:          "databox:manifests:app",
				Required:      true,
				Name:          "databox Apps",
				Clientid:      "APPS",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "apps",
						},
					},
					Href: "tcp://" + cm.AppStoreName + "-" + cm.CoreStoreName + ":5555/kv/apps",
				},
			},
			libDatabox.DataSource{
				Type:          "databox:manifests:driver",
				Required:      true,
				Name:          "databox Drivers",
				Clientid:      "DRIVERS",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "drivers",
						},
					},
					Href: "tcp://" + cm.AppStoreName + "-" + cm.CoreStoreName + ":5555/kv/drivers",
				},
			},
			libDatabox.DataSource{
				Type:          "databox:manifests:all",
				Required:      true,
				Name:          "databox manifests",
				Clientid:      "ALL",
				Granularities: []string{},
				Hypercat: libDatabox.HypercatItem{
					ItemMetadata: []interface{}{
						libDatabox.RelValPair{
							Rel: "urn:X-databox:rels:hasDatasourceid",
							Val: "all",
						},
					},
					Href: "tcp://" + cm.AppStoreName + "-" + cm.CoreStoreName + ":5555" + "/kv/all",
				},
			},
		},
	}

	err := cm.LaunchFromSLA(sla, false)
	libDatabox.ChkErr(err)

	_, err = cm.WaitForService(name, 10)
	libDatabox.ChkErr(err)

}
