const request      = require('request');
const url          = require('url');
const fs           = require('fs');
const promiseRetry = require('promise-retry');
const https        = require('https');

let https_agent = new https.Agent({ca: fs.readFileSync("/run/secrets/DATABOX_ROOT_CA")});

let DATABOX_NETWORK;
let DATABOX_NETWORK_ENDPOINT = "https://databox-network:8080";

const CM_KEY = fs.readFileSync("/run/secrets/DATABOX_NETWORK_KEY", {encoding: 'base64'});

module.exports = function(docker) {
    let module = {};

    /* create a network, get core-network's IP on that network, to serve as DNS resolver */
    const preConfig = async function(sla) {
        let networkName = sla.localContainerName + "-network";

        let internal = sla['databox-type'] === 'driver' ? false : true;
        let net = await getNetwork(networkName, internal);

        return new promiseRetry((retry, number) => {
            /* in case network is existed, inspect first, if no network spotted then connect */
            return net.inspect()
                .then((data) => {
                    let ipOnNet;
                    for(var contId in data.Containers) {
                        if (data.Containers[contId].Name === DATABOX_NETWORK) {
                            var cidr = data.Containers[contId].IPv4Address;
                            ipOnNet = cidr.split('/')[0];
                            break;
                        }
                    }

                    if (ipOnNet === undefined) {
                        /* newly created network, connect then retry */
                        return net.connect({"Container": DATABOX_NETWORK})
                            .then(() => {
                                console.log(DATABOX_NETWORK + " connected on " + networkName);
                                retry();
                            })
                            .catch(err => retry(err));
                    } else {
                        return {"NetworkName": networkName, "DNS": ipOnNet};
                    }
                }).catch(err => {
                    retry(err);
                });
            }, {retries: 3, factor: 1});
    };

    const connectEndpoints = async function(config, storeConfigArray, sla) {
        let toConnect = [];

        let configPeers = peersOfEnvArray(config.TaskTemplate.ContainerSpec.Env);
        if (configPeers.lenght !== 0) toConnect.push(connectFor(config.Name, configPeers));

        if (storeConfigArray !== false) {
            for (let storeConfig of storeConfigArray) {
                let storePeers = peersOfEnvArray(storeConfig.TaskTemplate.ContainerSpec.Env);
                if (storePeers.length !== 0) toConnect.push(connectFor(storeConfig.Name, storePeers));
            }
        }

        //parse 'external-whitelist' field from SLA
        if(sla['external-whitelist']) {
            let external = [];
            sla['external-whitelist'].forEach((itm) => {
                itm.urls.forEach((_url) => {
                    let hostname = url.parse(_url).hostname;
                    if (!external.includes(hostname)) external.push(hostname);
                });
            });

            if(external.length !== 0) {
                console.log("Connecting ", config.Name, " and externals: ", external);
                toConnect.push(connectFor(config.Name, external));
            }
        }

        return Promise.all(toConnect);
    };

    const identifyCM = async function() {
        let opt = {filters: {"name": ["container-manager"]}};

        return docker.listContainers(opt)
            .then(containers => {
                if (containers.length == 0) {
                    console.log("WARN: no CM found for core-network");
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

    const identifySelf = async function() {
        let opt = {filters: {"name": ["databox-network"]}};
        return docker.listContainers(opt)
            .then(containers => {
                if (containers.length === 0) {
                    console.log("ERR: no databox-network found");
                    return;
                }

                DATABOX_NETWORK = containers[0].Names[0].substring(1);
                console.log("set DATABOX_NETWORK to ", DATABOX_NETWORK);
                return;

            });
    };

    const networkOfService = function(service) {
        let config = {};
        return docker.getService(service).inspect()
            .then((data) => {
                let networks = data.Spec.Networks;
                if (networks.length !== 1) {
                    console.log(service + " on " + networks.length + " networks");
                }

                config["Network"] = networks[0].Target;
                return config["Network"];
            })
            .then((net) => {
                return docker.getNetwork(net).inspect()
                    .then((data) => {
                        for (let contId in data.Containers) {
                            if (toServiceName(data.Containers[contId].Name) === service) {
                                return data.Containers[contId].IPv4Address;
                            }
                        }
                    });
            })
            .then((ip) => {
                config["IP"] = ip;
                console.log(config);
                return config;
            });
    };

    const postUninstall = async function(service, config) {
        return disconnectFor(service, config.IP)
            .then(() => docker.getNetwork(config.Network).inspect())
            .then((data) => {
                let containers = [];
                for (let contId in data.Containers) {
                    let sname = toServiceName(data.Containers[contId].Name);
                    if (sname !== service && sname !== DATABOX_NETWORK) {
                        containers.push(data.Containers[contId].Name);
                    }
                }

                if (containers.length === 0) {
                    return Promise.resolve(docker.getNetwork(config.Network))
                        .then((net) => net.disconnect({"Container": DATABOX_NETWORK}))
                        .then((net) => removeNetwork(net))
                        .then(() => console.log("network ", config.Network, " removed"))
                        .catch((err) => console.log(err));
                } else {
                    for (let c of containers) {
                        console.log(c + " still on the network");
                    };
                    return;
                }
            })
            .catch(err => console.log("[core-network] postUninstall error ", err));

    };

    const getNetwork = async function(name, internal) {
        let config = {
            "Name": name,
            "Driver": "overlay",
            "Internal": internal,
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
                        console.log("using existed network");
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
                if (!peers.includes(hostname)) peers.push(hostname);
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
                url: DATABOX_NETWORK_ENDPOINT + "/connect",
                method: 'POST',
                body: data,
                agent: https_agent,
                json: true,
                headers: {
                    'x-api-key': CM_KEY
                }
            };
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

    const disconnectFor = function(name, ip) {
        return new Promise((resolve, reject) => {
            const data = {
                name: name,
                ip: ip
            };

            const options = {
                url: DATABOX_NETWORK_ENDPOINT + "/disconnect",
                method: 'POST',
                body: data,
                agent: https_agent,
                json: true,
                headers: {
                    'x-api-key': CM_KEY
                }
            };
            request(
                options,
                function(err, res, body) {
                    if (err || (res.statusCode < 200 || res.statusCode >= 300)) {
                        reject(err || new Error(body || "[disconnectFor] error: " + res.statusCode));
                        return;
                    }
                    console.log("[disconnectFor] " + name + " DONE");
                    resolve(body);
                });
        });
    };

    const addPrivileged = function(cmIP) {
        return new promiseRetry((retry) => {
            const data = {
                src_ip: cmIP
            };

            const options = {
                url: DATABOX_NETWORK_ENDPOINT + "/privileged",
                method: 'POST',
                body: data,
                agent: https_agent,
                json: true,
                headers: {
                   'x-api-key': CM_KEY
                }
            };
            request(
                options,
                function(err, res, body) {
                    if (err || (res.statusCode < 200 || res.statusCode >= 300)) {
                        console.log("[addPrivileged] ", err || res);
                        retry()
                    } else {
                        console.log("[addPrivileged] DONE");
                    }
                    return;}
            );
        }, {retries: 3, factor: 1});
    };

    const toServiceName = function(name) {
        let dot = name.indexOf('.');
        if (dot === -1) return name;
        else return name.substring(0, dot);
    }

    const removeNetwork = function(net) {
        const retryLimit = 3;
        const tryRemove = (count) => {
            return net.remove()
                .catch(err => {
                    return count >= retryLimit
                        ? Promise.reject(new Error("can't remove network"))
                        : setTimeout(() => tryRemove(count + 1), 1500);
                });
        };

        return tryRemove(1);
    };

    module.preConfig        = preConfig;
    module.connectEndpoints = connectEndpoints;
    module.identifyCM       = identifyCM;
    module.identifySelf     = identifySelf;
    module.networkOfService = networkOfService;
    module.postUninstall    = postUninstall;
    return module;
};
