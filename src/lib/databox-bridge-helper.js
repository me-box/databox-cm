const request      = require('request');
const url          = require('url');
const fs           = require('fs');
const promiseRetry = require('promise-retry');

const DATABOX_BRIDGE          = "bridge";
const DATABOX_BRIDGE_ENDPOINT = "http://bridge:8080";

const CM_KEY = fs.readFileSync("/run/secrets/CM_KEY", {encoding: 'base64'});

module.exports = function(docker) {
    let module = {};

    /* create a network, get bridge's IP on that network, to serve as DNS resolver */
    const preConfig = async function(sla) {
        let networkName = sla.localContainerName + "-bridge";
        let net = await createNetwork(networkName);

        return new promiseRetry(function(retry) {
            return net.connect({"Container": DATABOX_BRIDGE})
                .then(() => net.inspect())
                .then((data) => {
                    let ipOnNet;
                    for(var contId in data.Containers) {
                        if (data.Containers[contId].Name === DATABOX_BRIDGE) {
                            var cidr = data.Containers[contId].IPv4Address;
                            ipOnNet = cidr.split('/')[0];
                            break;
                        }
                    }

                    if (ipOnNet === undefined) return Promise.reject("no DNS");
                    else return Promise.resolve({"NetworkName": networkName, "DNS": ipOnNet});
                }).catch(err => {
                    retry(err);
                });
            }, {retries: 3, factor: 1});
    };

    const connectEndpoints = async function(config, storeConfigArray) {
        let toConnect = [];

        let configPeers = peersOfEnvArray(config.TaskTemplate.ContainerSpec.Env);
        if (configPeers.lenght !== 0) toConnect.push(connectFor(config.Name, configPeers));

        if (storeConfigArray !== false) {
            for (let storeConfig of storeConfigArray) {
                let storePeers = peersOfEnvArray(storeConfig.TaskTemplate.ContainerSpec.Env);
                if (storePeers.length !== 0) toConnect.push(connectFor(storeConfig.Name, storePeers));
            }
        }

        return Promise.all(toConnect);
    };

    const identifyCM = async function() {
        let opt = {filters: {"name": ["container-manager"]}};

        return docker.listContainers(opt)
            .then(containers => {
                if (containers.length == 0) {
                    console.log("WARN: no CM found for bridge");
                    return;
                }

                if (containers.length > 1) {
                    console.log("WARN: more than one CM found, taking the first");
                }

                const cm = containers[0];
                let cmIP = cm.NetworkSettings.Networks["databox-system-net"].IPAddress;
                return addPrivileged(cmIP);
            });
    };

    const createNetwork = async function(name) {
        let config = {
            "Name": name,
            "Driver": "overlay",
            "Internal": true,
            "Attachable": true
        };

        return docker.listNetworks({filters: {"name": [config.Name]}})
            .then(nets => {
                if (nets.length === 0) {
                    console.log("creating network ", config.Name);
                    console.log(config);
                    return docker.createNetwork(config)
                        .catch((err) => {
                            console.log("[ERROR] creating network ", name, err);
                        });
                } else {
                    let filter_fn = net => {
                        return net.Driver === config.Driver
                            && net.Internal === config.Internal
                            && net.Attachable === config.Attachable;
                    };
                    let filtered = nets.filter(filter_fn);

                    if (filtered.length >= 1) {
                        let existed = filtered[0];
                        return Promise.resolve(docker.getNetwork(existed.Id));
                    } else {
                        let err = new Error('[Error] network ' + config.Name + ' already exists!');
                        return Promise.reject(err);
                    }
                }
            });
    };

    const peersOfEnvArray = function(envArray) {
        let peers = [];

        for (let env of envArray) {
            let inx = env.indexOf('=');
            let key = env.substring(0, inx);
            let value = env.substring(inx + 1);
            let hostname;

            if (key === "DATABOX_ARBITER_ENDPOINT" ||
                key === "DATABOX_EXPORT_SERVICE_ENDPOINT" ||
                (key.startsWith("DATABOX_") && key.endsWith("_ENDPOINT"))) {
                //arbiter, export service, and dependent stores
                hostname = url.parse(value).hostname;
                if (!peers.includes[hostname]) peers.push(hostname);
            } else if (key.startsWith("DATASOURCE_")) {
                //datasources
                //multiple sources may in the same store, checkou duplication
                let url_str = JSON.parse(value).href;
                if (url_str === undefined || url_str === '') continue;
                else {
                    hostname = url.parse(url_str).hostname;
                    if (!peers.includes(hostname)) peers.push(hostname);
                }
            } else {
                //ignore DATABOX_LOCAL_NAME
                continue;
            }
        }

        return peers;
    };

    const connectFor = function(name, peers) {
        return new Promise((resolve, reject) => {
            const data = {
                name: name,
                peers: peers
            };

            const options = {
                url: DATABOX_BRIDGE_ENDPOINT + "/connect",
                method: 'POST',
                body: data,
                json: true,
                headers: {
                    'x-api-key': CM_KEY
                }
            };
            console.log(options);
            request(
                options,
                function(err, res, body) {
                    if (err || (res.statusCode < 200 || res.statusCode >= 300)) {
                        reject(err || body || "[connectFor] error: " + res.statusCode);
                        return;
                    }
                    console.log("[connectFor] " + name + " DONE");
                    resolve(body);
                });
        });
    };

    const addPrivileged = function(cmIP) {
        return new Promise((resolve, reject) => {
            const data = {
                src_ip: cmIP
            };

            const options = {
                url: DATABOX_BRIDGE_ENDPOINT + "/privileged",
                method: 'POST',
                body: data,
                json: true,
                headers: {
                    'x-api-key': CM_KEY
                }
            };
            console.log(options);
            request(
                options,
                function(err, res, body) {
                    if (err || (res.statusCode < 200 || res.statusCode >= 300)) {
                        console.log(err || body || "[addPrivileged] error: " + res.statusCode);
                    } else {
                        console.log("[addPrivileged] DONE");
                    }
                    resolve();
                });
        });
    };

    module.preConfig        = preConfig;
    module.connectEndpoints = connectEndpoints;
    module.identifyCM      = identifyCM;
    return module;
};
