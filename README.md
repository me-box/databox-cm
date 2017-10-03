
# Databox Container Manager

Databox container manager and dashboard are the part of the databox platform.
see [the main repository](https://github.com/me-box/databox) for more information. 

For developing Databox core components - Container Manager (CM) exposes following functions:
1. `setHttpsHelper(helper)`: this function provides a https-agent with Databox root certificate, so that arbitor accepts   requests by the https-agent.
2. `install(sla)`: start a app/driver service as a docker container.
3. `uninstall(service)`: remove the running `service` docker container.
4. `restart(container)`: restart the `container`.
5. `connect()`: this function checks if CM can connect to docker.
5. `listContainers()`: this list all Databox componentn containers.
6. `generateArbiterToken(name)`:  this function generates token to be passed to arbitor for the service.
7. `updateArbiter(data)`:  this function updates arbitor endpoint:/cm/: upsert-container-info using post 'data'
8. `restoreContainers(slas)`:  this function restores containers by relaunching them by their sla's.
9. `getActiveSLAs()`: this function gives all SLA's registered in the SLA - database.

### CM SLA database functions
10. `getSLA(name)`: find sla with `name` in `./slaStore/sladatastore.db`
11. `getAllSLAs`: list all slas in `./slaStore/sladatastore.db`
12. `putSLA(name, sla)`: put sla with `name` in `./slaStore/sladatastore.db`
13. `deleteSLA(name)`: delete sla with `name` from `./slaStore/sladatastore.db`

### CM network functions using docker network
14. `createNetwork(networkName, external)`: this function creates a docker network with name `networkName` and boolean type         `external` variable. If `external` is true, it means external excess to the network is allowed. 
15. `connectToNetwork(container, networkName)`: this function connects a container to the docker network -`networkName`
16. `disconnectFromNetwork(container, networkName)`: this function disconnects a container from the docker network -          `networkName`




