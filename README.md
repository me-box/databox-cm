
# Databox Container Manager

Databox container manager and dashboard user interface part of the databox platform 
see [the main repository](https://github.com/me-box/databox) for more information. 

For developing Databox core components- Databox exposes following functions:
1. `setHttpsHelper(helper)`: this functions https agent -with Databox root cirtificate.
2. `install(sla)`: start a app/driver service as a docker container.
3. `uninstall(service)`: remove the running `service` docker container.
4. `restart(container)`: restart the `container`.
5. `connect()`: this function checks if CM can connect to docker.
5. `listContainers()`: this list all Databox componentn containers.
6. `generateArbiterToken(name)`:  this function generates token to be passed to arbitor for the service.
7. `updateArbiter(data)`:  this function updates arbitor endpoint:/cm/upsert-container-info using post 'data'
8. `restoreContainers(slas)`:  this funtion restores containers by relaunching them by their sla's.
9. `getActiveSLAs()`: this function gives all SLA's registered in the SLA - database.



