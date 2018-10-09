package main

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	libDatabox "github.com/me-box/lib-go-databox"
)

//pullImageIfRequired will try and pull the image form DefaultRegistry if it dose not exist locally.
// If the image is taged latest then it will allways attempt to pull the image.
func pullImageIfRequired(image string, DefaultRegistry string, DefaultRegistryHost string) {
	needToPull := true
	ctx := context.Background()
	cli, _ := client.NewEnvClient()

	//do we have the image on disk?
	images, _ := cli.ImageList(ctx, types.ImageListOptions{})
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
	if strings.Contains(image, DefaultRegistry) == true && strings.Contains(image, ":latest") == true {
		//its in the default registry and has the :latest tag lets pull it to make sure we are up-to-date
		needToPull = true
	}

	if needToPull == true {
		libDatabox.Info("Pulling Image " + image)
		reader, err := cli.ImagePull(ctx, DefaultRegistryHost+"/"+image, types.ImagePullOptions{})
		if err != nil {
			libDatabox.Warn(err.Error())
			return
		}
		io.Copy(ioutil.Discard, reader)
		libDatabox.Info("Done pulling Image " + image)
		reader.Close()
	}
}

// copyFileToContainer copies a single file of any format to the target container
// dockers CopyToContainer only works with tar archives.
func copyFileToContainer(targetFullPath string, fileReader io.Reader, containerID string) error {

	cli, _ := client.NewEnvClient()
	ctx := context.Background()

	fileBody, _ := ioutil.ReadAll(fileReader)

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{
		Name: targetFullPath,
		Mode: 0660,
		Size: int64(len(fileBody)),
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(fileBody); err != err {
		return err
	}

	var r io.Reader
	r = &tarBuf
	err := cli.CopyToContainer(ctx, containerID, "/", r, types.CopyToContainerOptions{})
	if err != nil {
		return err
	}
	return nil
}

func createSecretIfNotExists(name, data string) string {

	cli, _ := client.NewEnvClient()
	ctx := context.Background()

	filters := filters.NewArgs()
	filters.Add("name", name)
	secrestsList, _ := cli.SecretList(ctx, types.SecretListOptions{Filters: filters})
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
	secretCreateResponse, err := cli.SecretCreate(ctx, secret)
	libDatabox.ChkErr(err)

	return secretCreateResponse.ID
}

func createSecretFromFileIfNotExists(name, dataPath string) string {

	data, _ := ioutil.ReadFile(dataPath)

	return createSecretIfNotExists(name, string(data))
}

func removeContainer(name string) {

	cli, _ := client.NewEnvClient()
	ctx := context.Background()

	filters := filters.NewArgs()
	filters.Add("name", name)
	containers, cerr := cli.ContainerList(ctx, types.ContainerListOptions{
		Filters: filters,
		All:     true,
	})
	libDatabox.ChkErrFatal(cerr)

	if len(containers) > 0 {
		rerr := cli.ContainerRemove(ctx, containers[0].ID, types.ContainerRemoveOptions{Force: true})
		libDatabox.ChkErr(rerr)
	}
}

func constructDefaultServiceSpec(localContainerName string, imageName string, databoxType libDatabox.DataboxType, databoxVersion string, netConf NetworkConfig) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Labels: map[string]string{"databox.type": string(databoxType)},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Hostname: localContainerName,
				Image:    imageName,
				Labels:   map[string]string{"databox.type": string(databoxType)},

				Env: []string{
					"DATABOX_ARBITER_ENDPOINT=tcp://arbiter:4444",
					"DATABOX_LOCAL_NAME=" + localContainerName,
					"DATABOX_VERSION=" + databoxVersion,
				},
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
}
