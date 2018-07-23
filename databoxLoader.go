package main

import (
	"context"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"time"

	libDatabox "github.com/toshbrown/lib-go-databox"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
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
	//start the core containers
	d.startCoreNetwork()

	//Create global secrets that are used in more than one container
	libDatabox.Debug("Creating secrets")
	d.DATABOX_ROOT_CA_ID = d.createSecretFromFileIfNotExists("DATABOX_ROOT_CA", "./certs/containerManagerPub.crt")
	d.CM_KEY_ID = d.createSecretFromFileIfNotExists("CM_KEY", "./certs/arbiterToken-container-manager")

	d.DATABOX_ARBITER_ID = d.createSecretFromFileIfNotExists("DATABOX_ARBITER.pem", "./certs/arbiter.pem")

	d.DATABOX_PEM = d.createSecretFromFileIfNotExists("DATABOX.pem", "./certs/container-manager.pem") //TODO sort out certs!!
	d.DATABOX_NETWORK_KEY = d.createSecretFromFileIfNotExists("DATABOX_NETWORK_KEY", "./certs/arbiterToken-databox-network")

	//make ZMQ secrests
	public, private, zmqErr := zmq.NewCurveKeypair()
	libDatabox.ChkErrFatal(zmqErr)
	d.ZMQ_PUBLIC_KEY_ID = d.createSecretIfNotExists("ZMQ_PUBLIC_KEY", public)
	d.ZMQ_SECRET_KEY_ID = d.createSecretIfNotExists("ZMQ_SECRET_KEY", private)

	//SET CM DNS, create secrets and join to databox-system-net will restart the ContainerManager if needed.
	d.updateContainerManager()

	d.startArbiter()
	d.startAppServer()

	return d.DATABOX_ROOT_CA_ID, d.ZMQ_PUBLIC_KEY_ID, d.ZMQ_SECRET_KEY_ID
}

func (d *Databox) getDNSIP() (string, error) {

	filters := filters.NewArgs()
	filters.Add("name", "databox-network")
	contList, _ := d.cli.ContainerList(context.Background(), types.ContainerListOptions{
		Filters: filters,
	})

	//after CM update we do not need to do this again!!
	if len(contList) > 0 {
		//store the databox-network IP to pass as dns server
		containerJSON, _ := d.cli.ContainerInspect(context.Background(), contList[0].ID)
		return containerJSON.NetworkSettings.Networks["databox-system-net"].IPAddress, nil
	}

	libDatabox.Err("getDNSIP ip not found")
	return "", errors.New("databox-network not found")
}

func (d *Databox) startCoreNetwork() {

	filters := filters.NewArgs()
	filters.Add("name", "databox-network")

	contList, _ := d.cli.ContainerList(context.Background(), types.ContainerListOptions{
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
	}

	_, err := d.cli.NetworkCreate(context.Background(), "databox-system-net", options)
	libDatabox.ChkErr(err)

	config := &container.Config{
		Image:  d.Options.CoreNetworkImage + ":" + d.Options.Version,
		Labels: map[string]string{"databox.type": "databox-network"},
		Cmd:    []string{"-f", "/tmp/relay"},
	}

	tokenPath := d.Options.HostPath + "/certs/arbiterToken-databox-network"
	pemPath := d.Options.HostPath + "/certs/databox-network.pem"
	BCASTFIFOPath := "/tmp/databox_relay"

	hostConfig := &container.HostConfig{
		Binds: []string{
			tokenPath + ":/run/secrets/DATABOX_NETWORK_KEY:rw",
			pemPath + ":/run/secrets/DATABOX_NETWORK.pem:rw",
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

	d.removeContainer(containerName)

	d.pullImageIfRequired(config.Image)

	containerCreateCreatedBody, ccErr := d.cli.ContainerCreate(context.Background(), config, hostConfig, networkingConfig, containerName)
	libDatabox.ChkErrFatal(ccErr)

	d.cli.ContainerStart(context.Background(), containerCreateCreatedBody.ID, types.ContainerStartOptions{})
	d.DATABOX_DNS_IP, _ = d.getDNSIP()

	//start core network relay
	d.startCoreNetworkRelay()
}

func (d *Databox) startCoreNetworkRelay() {

	config := &container.Config{
		Image:  d.Options.CoreNetworkRelayImage + ":" + d.Options.Version,
		Labels: map[string]string{"databox.type": "databox-network"},
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

	d.removeContainer(containerName)

	d.pullImageIfRequired(config.Image)

	containerCreateCreatedBody, ccErr := d.cli.ContainerCreate(context.Background(), config, hostConfig, &network.NetworkingConfig{}, containerName)
	libDatabox.ChkErrFatal(ccErr)

	d.cli.ContainerStart(context.Background(), containerCreateCreatedBody.ID, types.ContainerStartOptions{})
}

func (d *Databox) pullImageIfRequired(image string) {
	needToPull := true

	//do we have the image on disk?
	images, _ := d.cli.ImageList(context.Background(), types.ImageListOptions{})
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
	if strings.Contains(image, d.Options.DefaultRegistry) == true && strings.Contains(image, ":latest") == true {
		//its in the default registry and has the :latest tag lets pull it to make sure we are up-to-date
		needToPull = true
	}

	if needToPull == true {
		libDatabox.Info("Pulling Image " + image)
		reader, err := d.cli.ImagePull(context.Background(), d.Options.DefaultRegistryHost+"/"+image, types.ImagePullOptions{})
		libDatabox.ChkErrFatal(err)
		io.Copy(ioutil.Discard, reader)
		libDatabox.Info("Done pulling Image " + image)
		reader.Close()
	}
}

func (d *Databox) updateContainerManager() {

	//TODO error checking ;-)

	d.DATABOX_DNS_IP, _ = d.getDNSIP()

	filters := filters.NewArgs()
	filters.Add("name", "container-manager")

	swarmService, _ := d.cli.ServiceList(context.Background(), types.ServiceListOptions{
		Filters: filters,
	})

	if swarmService[0].Spec.TaskTemplate.ContainerSpec.DNSConfig != nil {
		//we have already updated the service!!!
		libDatabox.Debug("container-manager service is up to date")

		f, _ := os.OpenFile("/ect/resolv.conf", os.O_APPEND|os.O_WRONLY, os.ModeAppend)
		defer f.Close()
		f.WriteString("nameserver " + d.DATABOX_DNS_IP)

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
				Mode: 929,
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
				Mode: 929,
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
	time.Sleep(time.Second * 100)

}

func (d *Databox) startAppServer() {

	containerName := "app-server"

	config := &container.Config{
		Image: d.Options.AppServerImage + ":latest", // + d.version,
		Env:   []string{"LOCAL_MODE=1", "PORT=8181"},
		ExposedPorts: nat.PortSet{
			"8181/tcp": {},
		},
		Labels: map[string]string{"databox.type": "app-server"},
	}

	pemPath := d.Options.HostPath + "/certs/app-server.pem"

	ports := make(nat.PortMap)
	ports["8181/tcp"] = []nat.PortBinding{nat.PortBinding{HostPort: "8181"}}
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			mount.Mount{
				Type:   mount.TypeBind,
				Source: pemPath,
				Target: "/run/secrets/DATABOX.pem",
			},
		},
		PortBindings: ports,
	}
	networkingConfig := &network.NetworkingConfig{}

	d.removeContainer(containerName)

	d.pullImageIfRequired(config.Image)

	containerCreateCreatedBody, ccErr := d.cli.ContainerCreate(context.Background(), config, hostConfig, networkingConfig, containerName)
	libDatabox.ChkErrFatal(ccErr)

	d.cli.ContainerStart(context.Background(), containerCreateCreatedBody.ID, types.ContainerStartOptions{})
}

func (d *Databox) startArbiter() {

	service := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": "system"},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: d.Options.ArbiterImage + ":" + d.Options.Version,
				Secrets: []*swarm.SecretReference{
					&swarm.SecretReference{
						SecretID:   d.CM_KEY_ID,
						SecretName: "CM_KEY",
						File: &swarm.SecretReferenceFileTarget{
							Name: "CM_KEY",
							UID:  "0",
							GID:  "0",
							Mode: 929,
						},
					},
					&swarm.SecretReference{
						SecretID:   d.ZMQ_SECRET_KEY_ID,
						SecretName: "ZMQ_SECRET_KEY",
						File: &swarm.SecretReferenceFileTarget{
							Name: "ZMQ_SECRET_KEY",
							UID:  "0",
							GID:  "0",
							Mode: 929,
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

	d.pullImageIfRequired(service.TaskTemplate.ContainerSpec.Image)

	_, err := d.cli.ServiceCreate(context.Background(), service, serviceOptions)
	libDatabox.ChkErrFatal(err)

}

func (d *Databox) createSecretIfNotExists(name, data string) string {

	filters := filters.NewArgs()
	filters.Add("name", name)
	secrestsList, _ := d.cli.SecretList(context.Background(), types.SecretListOptions{Filters: filters})
	if len(secrestsList) > 0 {
		//we have made this before just return the ID
		return secrestsList[0].ID
	}

	secret := swarm.SecretSpec{
		Annotations: swarm.Annotations{
			Name: name,
		},
		Data: []byte(data),
	}
	libDatabox.Debug("createSecret for " + name)
	secretCreateResponse, err := d.cli.SecretCreate(context.Background(), secret)
	libDatabox.ChkErrFatal(err)

	return secretCreateResponse.ID
}

func (d *Databox) createSecretFromFileIfNotExists(name, dataPath string) string {

	data, _ := ioutil.ReadFile(dataPath)

	return d.createSecretIfNotExists(name, string(data))
}

func (d *Databox) removeContainer(name string) {
	filters := filters.NewArgs()
	filters.Add("name", name)
	containers, clerr := d.cli.ContainerList(context.Background(), types.ContainerListOptions{
		Filters: filters,
		All:     true,
	})
	libDatabox.ChkErrFatal(clerr)

	if len(containers) > 0 {
		rerr := d.cli.ContainerRemove(context.Background(), containers[0].ID, types.ContainerRemoveOptions{Force: true})
		libDatabox.ChkErrFatal(rerr)
	}
}
