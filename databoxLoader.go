package main

import (
	"context"
	"errors"
	"io/ioutil"
	"os"
	"strings"
	"time"

	libDatabox "github.com/me-box/lib-go-databox"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	zmq "github.com/pebbe/zmq4"
)

type Databox struct {
	cli                 *client.Client
	registry            string
	DATABOX_ROOT_CA_ID  string
	CM_KEY_ID           string
	DATABOX_ARBITER_ID  string
	ZMQ_SECRET_KEY_ID   string
	ZMQ_PUBLIC_KEY_ID   string
	DATABOX_PEM         string
	DATABOX_NETWORK_KEY string
	DATABOX_DNS_IP      string
	Options             *libDatabox.ContainerManagerOptions
}

func NewDataboxLoader(opt *libDatabox.ContainerManagerOptions) Databox {
	cli, _ := client.NewEnvClient()
	return Databox{
		cli:     cli,
		Options: opt,
	}
}

func (d *Databox) Start() (string, string, string) {

	libDatabox.Info("ContainerManager Started")

	// when the host is rebooted or docker daemon restarted
	// all the containers are restored but state is lost.
	// this leaves databox in a broken state. checkForAndFixBadRestarts
	// attempts to fix this problem
	badRestartDetected := d.checkForAndFixBadRestarts()

	//start the core containers
	d.startCoreNetwork()

	//Create global secrets that are used in more than one container
	libDatabox.Debug("Creating secrets")
	d.DATABOX_ROOT_CA_ID = createSecretFromFileIfNotExists("DATABOX_ROOT_CA", "./certs/containerManagerPub.crt")
	d.CM_KEY_ID = createSecretFromFileIfNotExists("CM_KEY", "./certs/arbiterToken-container-manager")

	d.DATABOX_ARBITER_ID = createSecretFromFileIfNotExists("DATABOX_ARBITER.pem", "./certs/arbiter.pem")

	d.DATABOX_PEM = createSecretFromFileIfNotExists("DATABOX.pem", "./certs/container-manager.pem")
	d.DATABOX_NETWORK_KEY = createSecretFromFileIfNotExists("DATABOX_NETWORK_KEY", "./certs/arbiterToken-databox-network")

	//make ZMQ secrests
	public, private, zmqErr := zmq.NewCurveKeypair()
	libDatabox.ChkErrFatal(zmqErr)
	d.ZMQ_PUBLIC_KEY_ID = createSecretIfNotExists("ZMQ_PUBLIC_KEY", public)
	d.ZMQ_SECRET_KEY_ID = createSecretIfNotExists("ZMQ_SECRET_KEY", private)

	//SET CM DNS, create secrets and join to databox-system-net will restart the ContainerManager if needed.
	d.updateContainerManager(badRestartDetected)

	d.startArbiter()

	return d.DATABOX_ROOT_CA_ID, d.ZMQ_PUBLIC_KEY_ID, d.ZMQ_SECRET_KEY_ID
}

func (d *Databox) checkForAndFixBadRestarts() bool {
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "databox.type")
	services, err := d.cli.ServiceList(ctx, types.ServiceListOptions{Filters: f})
	//cmServiceID := ""
	libDatabox.ChkErr(err)
	if len(services) > 1 { //the cm is running at this point so i would expect one service
		libDatabox.Warn("Container manager starting up but we have old databox services.")
		libDatabox.Warn("This was probably caused by a Container manager crash, host reboot, docker daemon restart")
		libDatabox.Warn("Waitling for docker to settle......")
		time.Sleep(time.Second * 15)
		libDatabox.Warn("Starting to clean up......")
		for _, service := range services {
			if service.Spec.Name == "container-manager" {
				//lets not kill ourselves, that's just silly
				libDatabox.Debug("Skipping container-manager")
				//cmServiceID = service.ID
				continue
			}
			libDatabox.Info("Removing old databox service " + service.Spec.Name)
			err := d.cli.ServiceRemove(ctx, service.ID)
			libDatabox.ChkErr(err)
		}

		removeContainer("databox-network")
		removeContainer("databox-network-relay")

		allNetworks, _ := d.cli.NetworkList(ctx, types.NetworkListOptions{Filters: f})
		if len(allNetworks) > 0 {
			for _, network := range allNetworks {
				libDatabox.Debug("Removing old databox network " + network.Name + " " + network.ID)
				netInfo, _ := d.cli.NetworkInspect(ctx, network.ID, types.NetworkInspectOptions{})
				for _, connected := range netInfo.Containers {
					libDatabox.Debug("Removing databox container form network before delete " + connected.Name)
					if strings.Contains(connected.Name, "container-manager.") {
						//lets not kill ourselves, that's just silly
						libDatabox.Debug("Skipping container-manager")
						continue
					}
					d.cli.NetworkDisconnect(ctx, network.ID, connected.Name, true)
				}
				time.Sleep(time.Second * 5)
				d.cli.NetworkRemove(ctx, network.ID)
			}
		}

		allSecrets, _ := d.cli.SecretList(ctx, types.SecretListOptions{})
		if len(allSecrets) > 0 {
			for _, sec := range allSecrets {
				if strings.Contains(sec.Spec.Name, "ZMQ_") {
					//dont delete the zmq keys
					continue
				}
				libDatabox.Debug("Removing old databox sec " + sec.Spec.Name + " " + sec.ID)
				err := d.cli.SecretRemove(ctx, sec.ID)
				libDatabox.ChkErr(err)
			}
		}

		libDatabox.Warn("Waitling for docker to recvover ......")
		time.Sleep(time.Second * 20)

		return true
	}

	return false
}

func (d *Databox) getDNSIP() (string, error) {

	filters := filters.NewArgs()
	filters.Add("name", "databox-network")
	contList, _ := d.cli.ContainerList(context.Background(), types.ContainerListOptions{
		Filters: filters,
	})

	if len(contList) > 0 {
		//store the databox-network IP to pass as dns server
		containerJSON, _ := d.cli.ContainerInspect(context.Background(), contList[0].ID)
		return containerJSON.NetworkSettings.Networks["databox-system-net"].IPAddress, nil
	}

	libDatabox.Err("getDNSIP ip not found")
	return "", errors.New("databox-network not found")
}

func (d *Databox) startCoreNetwork() {

	ctx := context.Background()

	filters := filters.NewArgs()
	filters.Add("name", "databox-network")

	contList, _ := d.cli.ContainerList(ctx, types.ContainerListOptions{
		Filters: filters,
	})

	//after CM update we do not need to do this again!!
	if len(contList) > 0 {
		libDatabox.Debug("databox-network already running")
		//store the databox-network IP to pass as dns server
		d.DATABOX_DNS_IP, _ = d.getDNSIP()
		return
	}

	libDatabox.Info("Starting databox-network")

	options := types.NetworkCreate{
		Driver:     "overlay",
		Attachable: true,
		Internal:   false,
		Labels:     map[string]string{"databox.type": "databox-network"},
	}

	_, err := d.cli.NetworkCreate(ctx, "databox-system-net", options)
	libDatabox.ChkErr(err)

	config := &container.Config{
		Image:  d.Options.CoreNetworkImage + "-" + d.Options.Arch + ":" + d.Options.Version,
		Labels: map[string]string{"databox.type": "databox-network"},
		Cmd:    []string{"-f", "/tmp/relay"},
	}

	BCASTFIFOPath := "/tmp/databox_relay"

	hostConfig := &container.HostConfig{
		Binds: []string{
			BCASTFIFOPath + ":/tmp/relay",
		},
		CapAdd: []string{"NET_ADMIN"},
	}
	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			"databox-system-net": &network.EndpointSettings{
				Aliases: []string{"databox-network"},
			},
		},
	}
	containerName := "databox-network"

	removeContainer(containerName)

	pullImageIfRequired(config.Image, d.Options.DefaultRegistry, d.Options.DefaultRegistryHost)

	containerCreateCreatedBody, ccErr := d.cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, containerName)
	libDatabox.ChkErrFatal(ccErr)

	f, err := os.Open("/certs/arbiterToken-databox-network")
	libDatabox.ChkErr(err)
	err = copyFileToContainer("/run/secrets/DATABOX_NETWORK_KEY", f, containerCreateCreatedBody.ID)
	f.Close()
	libDatabox.ChkErr(err)

	f, _ = os.Open("/certs/databox-network.pem")
	libDatabox.ChkErr(err)
	err = copyFileToContainer("/run/secrets/DATABOX_NETWORK.pem", f, containerCreateCreatedBody.ID)
	f.Close()
	libDatabox.ChkErr(err)

	err = d.cli.ContainerStart(context.Background(), containerCreateCreatedBody.ID, types.ContainerStartOptions{})
	libDatabox.ChkErrFatal(err)
	d.DATABOX_DNS_IP, _ = d.getDNSIP()

	libDatabox.Info("[startCoreNetwork] done starting databox-network")

	//start core network relay
	d.startCoreNetworkRelay()
}

func (d *Databox) startCoreNetworkRelay() {

	config := &container.Config{
		Image:  d.Options.CoreNetworkRelayImage + "-" + d.Options.Arch + ":" + d.Options.Version,
		Labels: map[string]string{"databox.type": "databox-network-relay"},
		Cmd:    []string{"-f", "/tmp/relay", "-h", d.Options.InternalIPs[0]}, //TODO can we pass all the IPs here?
	}

	BCASTFIFOPath := "/tmp/databox_relay"

	hostConfig := &container.HostConfig{
		Binds: []string{
			BCASTFIFOPath + ":/tmp/relay",
		},
		NetworkMode: "host",
	}

	containerName := "databox-broadcast-relay"

	removeContainer(containerName)

	pullImageIfRequired(config.Image, d.Options.DefaultRegistry, d.Options.DefaultRegistryHost)

	containerCreateCreatedBody, err := d.cli.ContainerCreate(context.Background(), config, hostConfig, &network.NetworkingConfig{}, containerName)
	libDatabox.ChkErrFatal(err)

	err = d.cli.ContainerStart(context.Background(), containerCreateCreatedBody.ID, types.ContainerStartOptions{})
	libDatabox.ChkErrFatal(err)
}

func (d *Databox) updateContainerManager(badRestartDetected bool) {

	libDatabox.Info("[updateContainerManager] called")

	d.DATABOX_DNS_IP, _ = d.getDNSIP()

	filters := filters.NewArgs()
	filters.Add("name", "container-manager")

	swarmService, _ := d.cli.ServiceList(context.Background(), types.ServiceListOptions{
		Filters: filters,
	})

	libDatabox.Info("[updateContainerManager] updating dns server")
	if swarmService[0].Spec.TaskTemplate.ContainerSpec.DNSConfig != nil && badRestartDetected == false {
		//we have already updated the service!!!
		libDatabox.Debug("container-manager service is up to date d.DATABOX_DNS_IP=" + d.DATABOX_DNS_IP)

		err := ioutil.WriteFile("/etc/resolv.conf", []byte("nameserver "+d.DATABOX_DNS_IP+"\noptions ndots:0 ndots:0\n"), 644)
		libDatabox.ChkErr(err)
		return
	}

	libDatabox.Debug("Updating container-manager Service " + d.DATABOX_DNS_IP)

	swarmService[0].Spec.TaskTemplate.ContainerSpec.DNSConfig = &swarm.DNSConfig{
		Nameservers: []string{d.DATABOX_DNS_IP},
		Options:     []string{"ndots:0"},
	}

	swarmService[0].Spec.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{
		swarm.NetworkAttachmentConfig{
			Target: "databox-system-net",
		},
	}
	swarmService[0].Spec.TaskTemplate.ContainerSpec.Secrets = append(
		swarmService[0].Spec.TaskTemplate.ContainerSpec.Secrets,
		&swarm.SecretReference{
			SecretID:   d.ZMQ_PUBLIC_KEY_ID,
			SecretName: "ZMQ_PUBLIC_KEY",
			File: &swarm.SecretReferenceFileTarget{
				Name: "ZMQ_PUBLIC_KEY",
				UID:  "0",
				GID:  "0",
				Mode: 0444,
			},
		})
	swarmService[0].Spec.TaskTemplate.ContainerSpec.Secrets = append(
		swarmService[0].Spec.TaskTemplate.ContainerSpec.Secrets,
		&swarm.SecretReference{
			SecretID:   d.DATABOX_ROOT_CA_ID,
			SecretName: "DATABOX_ROOT_CA",
			File: &swarm.SecretReferenceFileTarget{
				Name: "DATABOX_ROOT_CA",
				UID:  "0",
				GID:  "0",
				Mode: 0444,
			},
		})

	swarmService[0].Spec.TaskTemplate.ContainerSpec.Env = append(swarmService[0].Spec.TaskTemplate.ContainerSpec.Env, "DATABOX_DNS_IP="+d.DATABOX_DNS_IP)

	_, err := d.cli.ServiceUpdate(
		context.Background(),
		swarmService[0].ID,
		swarmService[0].Version,
		swarmService[0].Spec,
		types.ServiceUpdateOptions{},
	)
	libDatabox.ChkErrFatal(err)

	//waiting to be rebooted
	libDatabox.Info("Restarting the Container Manager")
	time.Sleep(time.Second * 1000)
}

func (d *Databox) startArbiter() {

	service := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": "system"},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: d.Options.ArbiterImage + "-" + d.Options.Arch + ":" + d.Options.Version,
				Secrets: []*swarm.SecretReference{
					&swarm.SecretReference{
						SecretID:   d.CM_KEY_ID,
						SecretName: "CM_KEY",
						File: &swarm.SecretReferenceFileTarget{
							Name: "CM_KEY",
							UID:  "0",
							GID:  "0",
							Mode: 0444,
						},
					},
					&swarm.SecretReference{
						SecretID:   d.ZMQ_SECRET_KEY_ID,
						SecretName: "ZMQ_SECRET_KEY",
						File: &swarm.SecretReferenceFileTarget{
							Name: "ZMQ_SECRET_KEY",
							UID:  "0",
							GID:  "0",
							Mode: 0444,
						},
					},
				},
			},
			Networks: []swarm.NetworkAttachmentConfig{swarm.NetworkAttachmentConfig{
				Target: "databox-system-net",
			}},
		},
	}

	service.Name = "arbiter"

	serviceOptions := types.ServiceCreateOptions{}

	pullImageIfRequired(service.TaskTemplate.ContainerSpec.Image, d.Options.DefaultRegistry, d.Options.DefaultRegistryHost)

	_, err := d.cli.ServiceCreate(context.Background(), service, serviceOptions)
	libDatabox.ChkErrFatal(err)

}
