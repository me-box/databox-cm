package main

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	libDatabox "github.com/toshbrown/lib-go-databox"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"

	"crypto/rand"
)

type ContainerManager struct {
	cli                *client.Client
	ArbiterClient      *libDatabox.ArbiterClient
	CoreNetworkClient  libDatabox.CoreNetworkClient
	CoreStoreClient    *libDatabox.CoreStoreClient
	Request            *http.Client
	DATABOX_DNS_IP     string
	DATABOX_ROOT_CA_ID string
	ZMQ_PUBLIC_KEY_ID  string
	ZMQ_PRIVATE_KEY_ID string
	ARCH               string
	cmStoreURL         string
	Logger             *libDatabox.Logger
	Store              *CMStore
	Options            *libDatabox.ContainerManagerOptions
}

// New returns a configured ContainerManager
func NewContainerManager(rootCASecretId string, zmqPublicId string, zmqPrivateId string, opt *libDatabox.ContainerManagerOptions) ContainerManager {

	cli, _ := client.NewEnvClient() //TODO store version in ContainerManagerOptions

	request := libDatabox.NewDataboxHTTPsAPIWithPaths("/certs/containerManager.crt")
	ac, err := libDatabox.NewArbiterClient("/certs/arbiterToken-container-manager", "/run/secrets/ZMQ_PUBLIC_KEY", "tcp://arbiter:4444")
	libDatabox.ChkErr(err)
	cnc := libDatabox.NewCoreNetworkClient("/certs/arbiterToken-databox-network", request)

	cm := ContainerManager{
		cli:                cli,
		ArbiterClient:      ac,
		CoreNetworkClient:  cnc,
		Request:            request,
		DATABOX_DNS_IP:     os.Getenv("DATABOX_DNS_IP"),
		DATABOX_ROOT_CA_ID: rootCASecretId,
		ZMQ_PUBLIC_KEY_ID:  zmqPublicId,
		ZMQ_PRIVATE_KEY_ID: zmqPrivateId,
		Options:            opt,
	}

	//TODO multi arch support is not available yet Speak to poonam about progress
	cm.ARCH = ""

	return cm
}

func (cm ContainerManager) Start() {

	//register with core-network
	cm.CoreNetworkClient.RegisterPrivileged()

	//wait for the arbiter
	_, err := cm.WaitForService("arbiter", 10)
	if err != nil {
		libDatabox.Err("Filed to register the cm with the arbiter. " + err.Error())
	}

	//register CM with arbiter - this is needed as the CM now has a store!
	err = cm.ArbiterClient.RegesterDataboxComponent("container-manager", cm.ArbiterClient.ArbiterToken, libDatabox.DataboxTypeApp)
	if err != nil {
		libDatabox.Err("Filed to register the cm with the arbiter. " + err.Error())
	}

	//launch the CM store
	cm.cmStoreURL = cm.launchCMStore()

	//setup the cm to log to the store
	cm.CoreStoreClient = libDatabox.NewCoreStoreClient(cm.ArbiterClient, "/run/secrets/ZMQ_PUBLIC_KEY", cm.cmStoreURL, false)
	l, err := libDatabox.New(cm.CoreStoreClient, cm.Options.EnableDebugLogging)
	if err != nil {
		libDatabox.Err("Filed to set up logging to store. " + err.Error())
	}
	cm.Logger = l
	cm.Logger.Debug("CM logs going to the cm store")

	//setup the cmStore
	cm.Store = NewCMStore(cm.CoreStoreClient)

	//clear the saved slas if needed
	if cm.Options.ClearSLAs && err == nil {
		libDatabox.Info("Clearing SLA database to remove saved apps and drivers")
		err = cm.Store.ClearSLADatabase()
		libDatabox.ChkErr(err)
	}

	//Load the password from the store or create a new one
	password := ""
	if cm.Options.OverridePasword != "" {
		libDatabox.Warn("OverridePasword used!")
		password = cm.Options.OverridePasword
	} else {
		libDatabox.Debug("Getting password from DB")
		password, err = cm.Store.LoadPassword()
		libDatabox.ChkErr(err)
		if password == "" {
			libDatabox.Debug("Password not set genorating one")
			password = cm.genoratePassword()
			libDatabox.Debug("Saving new Password")
			err := cm.Store.SavePassword(password)
			libDatabox.ChkErr(err)
		}
	}
	libDatabox.Info("Password=" + password)

	//start the webUI and API
	go ServeInsecure()
	go ServeSecure(&cm, password)

	libDatabox.Info("Container Manager Ready.")

	//Reload and saved drivers and app from the cm store
	libDatabox.Info("Restarting saved apps and drivers")
	cm.reloadApps()
}

// LaunchFromSLA will start a databox app or driver with the reliant stores and grant permissions required as described in the SLA
func (cm ContainerManager) LaunchFromSLA(sla libDatabox.SLA) error {

	//Make the localContainerName
	localContainerName := sla.Name + cm.ARCH

	//Make the requiredStoreName if needed
	//TODO check store is supported!!
	requiredStoreName := ""
	if sla.ResourceRequirements.Store != "" {
		requiredStoreName = sla.Name + "-" + sla.ResourceRequirements.Store + cm.ARCH
	}

	//Create the networks and attach to the core-network.
	netConf := cm.CoreNetworkClient.PreConfig(localContainerName, sla)

	//start the container
	var service swarm.ServiceSpec
	var serviceOptions types.ServiceCreateOptions
	var requiredNetworks []string
	switch sla.DataboxType {
	case libDatabox.DataboxTypeApp:
		service, serviceOptions, requiredNetworks = cm.getAppConfig(sla, localContainerName, netConf)
	case libDatabox.DataboxTypeDriver:
		service, serviceOptions, requiredNetworks = cm.getDriverConfig(sla, localContainerName, netConf)
	}

	//If we need a store lets create one and set the needed environment variables
	if requiredStoreName != "" {
		service.TaskTemplate.ContainerSpec.Env = append(
			service.TaskTemplate.ContainerSpec.Env,
			"DATABOX_ZMQ_ENDPOINT=tcp://"+requiredStoreName+":5555",
		)
		service.TaskTemplate.ContainerSpec.Env = append(
			service.TaskTemplate.ContainerSpec.Env,
			"DATABOX_ZMQ_DEALER_ENDPOINT=tcp://"+requiredStoreName+":5556",
		)
		cm.launchStore(sla.ResourceRequirements.Store, requiredStoreName, netConf)
		requiredNetworks = append(requiredNetworks, requiredStoreName)

		cm.WaitForService(requiredStoreName, 10)

	}

	libDatabox.Debug("networksToConnect" + strings.Join(requiredNetworks, ","))
	cm.CoreNetworkClient.ConnectEndpoints(localContainerName, requiredNetworks)

	cm.pullImage(service.TaskTemplate.ContainerSpec.Image)

	serviceCreateResponse, err := cm.cli.ServiceCreate(context.Background(), service, serviceOptions)
	if err != nil {
		libDatabox.Err("[Error launching] " + localContainerName + " " + err.Error())
		return err
	}

	fmt.Println("serviceCreateResponse.Warnings::", serviceCreateResponse.Warnings)

	cm.addPermissionsFromSLA(sla)

	//save the sla for persistence over restarts
	err = cm.Store.SaveSLA(sla)
	libDatabox.ChkErr(err)

	libDatabox.Info("Successfully installed " + sla.Name)

	return nil
}

func (cm *ContainerManager) pullImage(image string) {

	needToPull := true

	//do we have the image on disk?
	images, _ := cm.cli.ImageList(context.Background(), types.ImageListOptions{})
	for _, i := range images {
		for _, tag := range i.RepoTags {
			if image == tag {
				//we have the image no need to pull it !!
				needToPull = false
				break
			}
		}
	}

	//is it from the default registry (databoxsystems or whatever we overroad with) and tagged with latest?
	if strings.Contains(image, cm.Options.DefaultRegistry) == true && strings.Contains(image, ":latest") == true {
		//its in the default registry and has the :latest tag lets pull it to make sure we are up-to-date
		needToPull = true
	}

	if needToPull == true {
		libDatabox.Info("Pulling Image " + image)
		reader, err := cm.cli.ImagePull(context.Background(), cm.Options.DefaultRegistryHost+"/"+image, types.ImagePullOptions{})
		libDatabox.ChkErr(err)
		io.Copy(ioutil.Discard, reader)
		libDatabox.Info("Done pulling Image " + image)
		reader.Close()
	}

}

// Restart will restart the databox component, app or driver by service name
func (cm ContainerManager) Restart(name string) error {
	filters := filters.NewArgs()
	filters.Add("label", "com.docker.swarm.service.name="+name)

	contList, _ := cm.cli.ContainerList(context.Background(),
		types.ContainerListOptions{
			Filters: filters,
		})
	if len(contList) < 1 {
		return errors.New("Service " + name + " not running")
	}

	//Stash the old container IP
	oldIP := ""
	for netName := range contList[0].NetworkSettings.Networks {
		serviceName := strings.Replace(name, "-core-store", "", 1) //TODO hardcoded -core-store
		if strings.Contains(netName, serviceName) {
			oldIP = contList[0].NetworkSettings.Networks[netName].IPAMConfig.IPv4Address
			libDatabox.Debug("Old IP for " + netName + " is " + oldIP)
		}
	}

	//Stop the container then the service will start a new one
	err := cm.cli.ContainerRemove(context.Background(), contList[0].ID, types.ContainerRemoveOptions{Force: true})
	if err != nil {
		return errors.New("Cannot remove " + name + " " + err.Error())
	}

	//wait for the restarted container to start
	newCont, err := cm.WaitForService(name, 10)
	libDatabox.ChkErr(err)

	//found restarted container !!!
	//Stash the new container IP
	newIP := ""
	for netName := range newCont.NetworkSettings.Networks {
		serviceName := strings.Replace(name, "-core-store", "", 1)
		if strings.Contains(netName, serviceName) {
			newIP = newCont.NetworkSettings.Networks[netName].IPAMConfig.IPv4Address
			libDatabox.Debug("IP  IP for " + netName + " is " + newIP)
		}
	}

	return cm.CoreNetworkClient.ServiceRestart(name, oldIP, newIP)
}

// Uninstall will remove the databox app or driver by service name
func (cm ContainerManager) Uninstall(name string) error {

	serFilters := filters.NewArgs()
	serFilters.Add("name", name)
	serList, _ := cm.cli.ServiceList(context.Background(),
		types.ServiceListOptions{
			Filters: serFilters,
		})

	if len(serList) < 1 {
		return errors.New("Service " + name + " not running")
	}

	networkConfig, err := cm.CoreNetworkClient.NetworkOfService(serList[0], serList[0].Spec.Name)
	libDatabox.ChkErr(err)

	err = cm.cli.ServiceRemove(context.Background(), serList[0].ID)
	libDatabox.ChkErr(err)

	//remove secrets
	secFilters := filters.NewArgs()
	secFilters.Add("name", name)
	serviceList, err := cm.cli.ServiceList(context.Background(), types.ServiceListOptions{
		Filters: secFilters,
	})
	for _, s := range serviceList {
		for _, sec := range s.Spec.TaskTemplate.ContainerSpec.Secrets {
			libDatabox.Debug("Removing secrete " + sec.SecretName)
			cm.cli.SecretRemove(context.Background(), sec.SecretID)
		}
	}

	cm.CoreNetworkClient.PostUninstall(name, networkConfig)

	cm.Store.DeleteSLA(name)

	return err
}

// WaitForService will wait for a container to start searching for it by service name.
// If the container is found within the timeout it will return a docker/api/types.Container and nil
// otherwise an error will be returned.
func (cm ContainerManager) WaitForService(name string, timeout int) (types.Container, error) {
	libDatabox.Debug("Waiting for " + name)
	filters := filters.NewArgs()
	filters.Add("label", "com.docker.swarm.service.name="+name)

	//wait for the restarted container to start
	var contList []types.Container
	loopCount := 0
	for {

		contList, _ = cm.cli.ContainerList(context.Background(),
			types.ContainerListOptions{
				Filters: filters,
			})

		if len(contList) > 0 {
			//found something!!

			//give it a bit more time
			//TODO think about using /status here
			time.Sleep(time.Second * 3)
			break
		}

		time.Sleep(time.Second)
		loopCount++
		if loopCount > timeout {
			return types.Container{}, errors.New("Service has not restarted after " + strconv.Itoa(timeout) + " seconds !!")
		}
		continue

	}

	return contList[0], nil
}

func (cm ContainerManager) genoratePassword() string {

	b := make([]byte, 24)
	rand.Read(b)
	pass := b64.StdEncoding.EncodeToString(b)
	return pass

}

func (cm ContainerManager) reloadApps() {

	slaList, err := cm.Store.GetAllSLAs()
	libDatabox.ChkErr(err)
	libDatabox.Debug("reloadApps slaList len=" + strconv.Itoa(len(slaList)))
	//launch drivers
	for _, sla := range slaList {
		if sla.DataboxType == libDatabox.DataboxTypeDriver {
			cm.LaunchFromSLA(sla)
			dboxproxy.Add(sla.Name)
		}
	}

	//launch apps
	for _, sla := range slaList {
		if sla.DataboxType == libDatabox.DataboxTypeApp {
			cm.LaunchFromSLA(sla)
			dboxproxy.Add(sla.Name)
		}
	}

}

// launchCMStore start a core-store the the container manager to store its configuration
func (cm ContainerManager) launchCMStore() string {
	//startCMStore

	sla := libDatabox.SLA{
		Name:        "container-manager",
		DataboxType: "Driver",
		ResourceRequirements: libDatabox.ResourceRequirements{
			Store: "core-store",
		},
	}
	cm.launchStore("core-store", "container-manager-core-store", libDatabox.NetworkConfig{NetworkName: "databox-system-net", DNS: cm.DATABOX_DNS_IP})
	cm.addPermissionsFromSLA(sla)

	_, err := cm.WaitForService("container-manager-core-store", 10)
	libDatabox.ChkErr(err)

	return "tcp://container-manager-core-store:5555"
}

func (cm ContainerManager) calculateRegistryUrlFromSLA(sla libDatabox.SLA) string {

	//default to default registry
	registryUrl := cm.Options.DefaultRegistry + "/"

	if sla.Registry != "" {
		return sla.Registry + "/"
	}

	if sla.StoreURL != "" {
		storeURL, err := url.Parse(sla.StoreURL)
		if err != nil {
			libDatabox.Warn("Could not parse sla.StoreURL as a url. Using default registry")
			return registryUrl
		}
		storeHost := storeURL.Hostname()
		if storeHost == "127.0.0.1" || storeHost == "localhost" {
			//the sla came from the local store the image should be on the local machine
			registryUrl = ""
		}
	}

	return registryUrl
}

func (cm ContainerManager) getDriverConfig(sla libDatabox.SLA, localContainerName string, netConf libDatabox.NetworkConfig) (swarm.ServiceSpec, types.ServiceCreateOptions, []string) {

	registry := cm.calculateRegistryUrlFromSLA(sla)

	service := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": "driver"},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Hostname: localContainerName,
				Image:    registry + sla.Name + ":" + cm.Options.Version,
				Labels:   map[string]string{"databox.type": "driver"},

				Env: []string{
					"DATABOX_ARBITER_ENDPOINT=tcp://arbiter:4444",
					"DATABOX_LOCAL_NAME=" + localContainerName,
				},
				Secrets: cm.genorateSecrets(localContainerName, sla.DataboxType),
				DNSConfig: &swarm.DNSConfig{
					Nameservers: []string{netConf.DNS},
				},
			},
			Networks: []swarm.NetworkAttachmentConfig{swarm.NetworkAttachmentConfig{
				Target:  netConf.NetworkName,
				Aliases: []string{localContainerName},
			}},
			Placement: &swarm.Placement{
				Constraints: []string{"node.Role == manager"},
			},
		},
		EndpointSpec: &swarm.EndpointSpec{
			Mode: swarm.ResolutionModeDNSRR,
		},
	}

	service.Name = localContainerName

	serviceOptions := types.ServiceCreateOptions{}

	return service, serviceOptions, []string{"arbiter"}
}

func (cm ContainerManager) getAppConfig(sla libDatabox.SLA, localContainerName string, netConf libDatabox.NetworkConfig) (swarm.ServiceSpec, types.ServiceCreateOptions, []string) {

	registry := cm.calculateRegistryUrlFromSLA(sla)

	service := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": "app"},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Hostname: localContainerName,
				Image:    registry + sla.Name + ":" + cm.Options.Version,
				Labels:   map[string]string{"databox.type": "app"},

				Env: []string{
					"DATABOX_ARBITER_ENDPOINT=tcp://arbiter:4444",
					"DATABOX_LOCAL_NAME=" + localContainerName,
					"DATABOX_EXPORT_SERVICE_ENDPOINT=https://export-service:8080",
				},
				Secrets: cm.genorateSecrets(localContainerName, sla.DataboxType),
				DNSConfig: &swarm.DNSConfig{
					Nameservers: []string{netConf.DNS},
				},
			},
			Networks: []swarm.NetworkAttachmentConfig{swarm.NetworkAttachmentConfig{
				Target:  netConf.NetworkName,
				Aliases: []string{localContainerName},
			}},
			Placement: &swarm.Placement{
				Constraints: []string{"node.Role == manager"},
			},
		},
		EndpointSpec: &swarm.EndpointSpec{
			Mode: swarm.ResolutionModeDNSRR,
		},
	}

	service.Name = localContainerName

	serviceOptions := types.ServiceCreateOptions{}

	//add datasource info to the env vars and create a list networks this app needs to access
	requiredNetworks := map[string]string{"arbiter": "arbiter", "export-service": "export-service"}

	for _, ds := range sla.Datasources {
		hypercatString, _ := json.Marshal(ds.Hypercat)
		service.TaskTemplate.ContainerSpec.Env = append(
			service.TaskTemplate.ContainerSpec.Env,
			"DATASOURCE_"+ds.Clientid+"="+string(hypercatString),
		)
		parsedURL, _ := url.Parse(ds.Hypercat.Href)
		storeName := parsedURL.Hostname()
		requiredNetworks[storeName] = storeName
	}

	//connect to networks
	networksToConnect := []string{}
	if len(requiredNetworks) > 0 {
		for store := range requiredNetworks {
			networksToConnect = append(networksToConnect, store)
		}
	}

	return service, serviceOptions, networksToConnect
}

func (cm ContainerManager) launchStore(requiredStore string, requiredStoreName string, netConf libDatabox.NetworkConfig) string {

	//Check to see if the store already exists !!
	storeFilter := filters.NewArgs()
	storeFilter.Add("name", requiredStoreName)
	stores, _ := cm.cli.ServiceList(context.Background(), types.ServiceListOptions{Filters: storeFilter})
	if len(stores) > 0 {
		//we have made this before just return the requiredStoreName
		return requiredStoreName
	}

	service := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": "store"},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Hostname: requiredStoreName,
				Image:    cm.Options.DefaultStoreImage + ":" + cm.Options.Version,
				Labels:   map[string]string{"databox.type": "store"},
				Env: []string{
					"DATABOX_ARBITER_ENDPOINT=tcp://arbiter:4444",
					"DATABOX_LOCAL_NAME=" + requiredStoreName,
				},
				Secrets: cm.genorateSecrets(requiredStoreName, libDatabox.DataboxType("store")),
				DNSConfig: &swarm.DNSConfig{
					Nameservers: []string{netConf.DNS},
				},
				Mounts: []mount.Mount{
					mount.Mount{
						Source: requiredStoreName,
						Target: "/database",
						Type:   "volume",
					},
				},
			},
			Placement: &swarm.Placement{
				Constraints: []string{"node.role == manager"},
			},
			Networks: []swarm.NetworkAttachmentConfig{swarm.NetworkAttachmentConfig{
				Target:  netConf.NetworkName,
				Aliases: []string{requiredStoreName},
			}},
		},
		EndpointSpec: &swarm.EndpointSpec{
			Mode: swarm.ResolutionModeDNSRR,
		},
	}

	service.Name = requiredStoreName
	serviceOptions := types.ServiceCreateOptions{}

	storeName := requiredStoreName

	cm.pullImage(service.TaskTemplate.ContainerSpec.Image)

	_, err := cm.cli.ServiceCreate(context.Background(), service, serviceOptions)
	if err != nil {
		libDatabox.Err("Launching store " + requiredStoreName + " " + err.Error())
	}

	return storeName
}

func (cm ContainerManager) createSecret(name string, data []byte, filename string) *swarm.SecretReference {

	filters := filters.NewArgs()
	filters.Add("name", name)
	secrestsList, _ := cm.cli.SecretList(context.Background(), types.SecretListOptions{Filters: filters})
	if len(secrestsList) > 0 {
		//remove any secrets and install new one just in case
		for _, s := range secrestsList {
			cm.cli.SecretRemove(context.Background(), s.ID)
		}
	}

	secret := swarm.SecretSpec{
		Annotations: swarm.Annotations{
			Name: name,
		},
		Data: data,
	}

	secretCreateResponse, err := cm.cli.SecretCreate(context.Background(), secret)
	if err != nil {
		libDatabox.Err("addSecrets createSecret " + err.Error())
	}

	return &swarm.SecretReference{
		SecretID:   secretCreateResponse.ID,
		SecretName: name,
		File: &swarm.SecretReferenceFileTarget{
			Name: filename,
			UID:  "0",
			GID:  "0",
			Mode: 0444,
		},
	}
}

// genorateSecrets creates, if required, all the secrets passed to the databox app drivers and stores
func (cm ContainerManager) genorateSecrets(containerName string, databoxType libDatabox.DataboxType) []*swarm.SecretReference {

	secrets := []*swarm.SecretReference{}

	secrets = append(secrets, &swarm.SecretReference{
		SecretID:   cm.DATABOX_ROOT_CA_ID,
		SecretName: "DATABOX_ROOT_CA",
		File: &swarm.SecretReferenceFileTarget{
			Name: "DATABOX_ROOT_CA",
			UID:  "0",
			GID:  "0",
			Mode: 0444,
		},
	})

	secrets = append(secrets, &swarm.SecretReference{
		SecretID:   cm.ZMQ_PUBLIC_KEY_ID,
		SecretName: "ZMQ_PUBLIC_KEY",
		File: &swarm.SecretReferenceFileTarget{
			Name: "ZMQ_PUBLIC_KEY",
			UID:  "0",
			GID:  "0",
			Mode: 0444,
		},
	})

	cert := GenCert(
		"./certs/containerManager.crt", //TODO Fix this
		containerName,
		[]string{"127.0.0.1"},
		[]string{containerName},
	)
	secrets = append(secrets, cm.createSecret(strings.ToUpper(containerName)+".pem", cert, "DATABOX.pem"))

	rawToken := GenerateArbiterToken()
	b64TokenString := b64.StdEncoding.EncodeToString(rawToken)
	secrets = append(secrets, cm.createSecret(strings.ToUpper(containerName)+"_KEY", []byte(b64TokenString), "ARBITER_TOKEN"))

	//update the arbiter with the containers token
	libDatabox.Debug("addSecrets UpdateArbiter " + containerName + " " + b64TokenString + " " + string(databoxType))
	err := cm.ArbiterClient.RegesterDataboxComponent(containerName, b64TokenString, databoxType)
	if err != nil {
		libDatabox.Err("Add Secrets error updating arbiter " + err.Error())
	}

	//Only pass the zmq private key to stores.
	if databoxType == "store" {
		libDatabox.Debug("[addSecrets] ZMQ_PRIVATE_KEY_ID=" + cm.ZMQ_PRIVATE_KEY_ID)
		secrets = append(secrets, &swarm.SecretReference{
			SecretID:   cm.ZMQ_PRIVATE_KEY_ID,
			SecretName: "ZMQ_SECRET_KEY",
			File: &swarm.SecretReferenceFileTarget{
				Name: "ZMQ_SECRET_KEY",
				UID:  "0",
				GID:  "0",
				Mode: 0444,
			},
		})
	}

	return secrets
}

//addPermissionsFromSLA parses a databox SLA and updates the arbiter with the correct permissions
func (cm ContainerManager) addPermissionsFromSLA(sla libDatabox.SLA) {

	var err error

	localContainerName := sla.Name + cm.ARCH

	//set export permissions from export-whitelist
	if len(sla.ExportWhitelists) > 0 {
		urlsString := "destination = \""
		for i, whiteList := range sla.ExportWhitelists {
			urlsString = urlsString + whiteList.Url
			if i < len(sla.ExportWhitelists) {
				urlsString = urlsString + ","
			}
		}
		urlsString = urlsString + "\""

		libDatabox.Debug("Adding Export permissions for " + localContainerName + " on " + urlsString)

		err = cm.addPermission(localContainerName, "export-service", "/export/", "POST", []string{urlsString})
		if err != nil {
			libDatabox.Err("Adding write permissions for export-service " + err.Error())
		}
	}

	//set export permissions from ExternalWhitelist
	if sla.DataboxType == "driver" && len(sla.ExternalWhitelist) > 0 {
		//TODO move this logic to the coreNetworkClient
		for _, whiteList := range sla.ExternalWhitelist {
			libDatabox.Debug("addPermissionsFromSla adding ExternalWhitelist for " + localContainerName + " on " + strings.Join(whiteList.Urls, ", "))
			externals := []string{}
			for _, u := range whiteList.Urls {
				parsedURL, err := url.Parse(u)
				if err != nil {
					libDatabox.Warn("Error parsing url in ExternalWhitelist")
				}
				externals = append(externals, parsedURL.Hostname())
			}
			err := cm.CoreNetworkClient.ConnectEndpoints(localContainerName, externals)
			libDatabox.ChkErr(err)
		}
	}

	//set read permissions from the sla for DATASOURCES.
	if sla.DataboxType == "app" && len(sla.Datasources) > 0 {

		for _, ds := range sla.Datasources {
			datasourceEndpoint, _ := url.Parse(ds.Hypercat.Href)
			datasourceName := datasourceEndpoint.Path

			//Deal with Actuators
			isActuator := false
			for _, item := range ds.Hypercat.ItemMetadata {
				switch item.(type) {
				case libDatabox.RelValPairBool:
					if item.(libDatabox.RelValPairBool).Rel == "urn:X-databox:rels:isActuator" && item.(libDatabox.RelValPairBool).Val == true {
						isActuator = true
						break
					}
				default:
					// we are only interested in libDatabox.RelValPairBool
				}
			}
			if isActuator == true {
				libDatabox.Debug("Adding write permissions for Actuator " + datasourceName + " on " + datasourceEndpoint.Hostname())
				err = cm.addPermission(localContainerName, datasourceEndpoint.Hostname(), "/"+datasourceName+"/*", "POST", []string{})
				if err != nil {
					libDatabox.Err("Adding write permissions for Actuator " + err.Error())
				}
			}

			libDatabox.Debug("Adding read permissions for /status  on " + datasourceEndpoint.Hostname())
			err = cm.addPermission(localContainerName, datasourceEndpoint.Hostname(), "/status", "GET", []string{})
			if err != nil {
				libDatabox.Err("Adding write permissions for Datasource " + err.Error())
			}

			libDatabox.Debug("Adding read permissions for " + localContainerName + " on data source " + datasourceName + " on " + datasourceEndpoint.Hostname())
			err = cm.addPermission(localContainerName, datasourceEndpoint.Hostname(), datasourceName, "GET", []string{})
			if err != nil {
				libDatabox.Err("Adding write permissions for Datasource " + err.Error())
			}

			libDatabox.Debug("Adding read permissions for " + localContainerName + " on data source " + datasourceName + " on " + datasourceEndpoint.Hostname() + "/*")
			err = cm.addPermission(localContainerName, datasourceEndpoint.Hostname(), datasourceName+"/*", "GET", []string{})
			if err != nil {
				libDatabox.Err("Adding write permissions for Datasource " + err.Error())
			}

		}

	}

	//Add permissions for dependent stores if needed for apps and drivers
	if sla.ResourceRequirements.Store != "" {
		requiredStoreName := sla.Name + "-" + sla.ResourceRequirements.Store + cm.ARCH

		libDatabox.Debug("Adding read permissions for container-manager on " + requiredStoreName + "/cat")
		err = cm.addPermission("container-manager", requiredStoreName, "/cat", "GET", []string{})
		if err != nil {
			libDatabox.Err("Adding read permissions for container-manager on /cat " + err.Error())
		}

		libDatabox.Debug("Adding write permissions for dependent store " + localContainerName + " on " + requiredStoreName + "/*")
		err = cm.addPermission(localContainerName, requiredStoreName, "/*", "POST", []string{})
		if err != nil {
			libDatabox.Err("Adding write permissions for dependent store " + err.Error())
		}

		libDatabox.Debug("Adding delete permissions for dependent store " + localContainerName + " on " + requiredStoreName + "/*")
		err = cm.addPermission(localContainerName, requiredStoreName, "/*", "DELETE", []string{})
		if err != nil {
			libDatabox.Err("Adding delete permissions for dependent store " + err.Error())
		}

		err = cm.addPermission(localContainerName, requiredStoreName, "/*", "GET", []string{})
		if err != nil {
			libDatabox.Err("Adding read permissions for dependent store " + err.Error())
		}

	}
}

//addPermission helper function to wraps ArbiterClient.GrantContainerPermissions
func (cm ContainerManager) addPermission(name string, target string, path string, method string, caveats []string) error {

	newPermission := libDatabox.ContainerPermissions{
		Name: name,
		Route: libDatabox.Route{
			Target: target,
			Path:   path,
			Method: method,
		},
		Caveats: caveats,
	}

	return cm.ArbiterClient.GrantContainerPermissions(newPermission)

}
