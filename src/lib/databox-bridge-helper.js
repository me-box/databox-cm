const request = require('request');
const url     = require('url');
const fs      = require('fs');

const DATABOX_BRIDGE          = "bridge";
const DATABOX_BRIDGE_ENDPOINT = "http://bridge:8080";

const CM_KEY = fs.readFileSync("/run/secrets/CM_KEY", {encoding: 'base64'});

module.exports = function(docker) {
    let module = {};

    const connectEndpoints = async function(config, storeConfigArray) {
        let toConnect = [];

        let configPeers = peersOfEnvArray(config.TaskTemplate.ContainerSpec.Env);
        if (configPeers.lenght !== 0) toConnect.push(connectFor(config.Name, configPeers));

        for (let storeConfig of storeConfigArray) {
            let storePeers = peersOfEnvArray(storeConfig.TaskTemplate.ContainerSpec.Env);
            if (storePeers.length !== 0) toConnect.push(connectFor(storeConfig.Name, storePeers));
        }

        return Promise.all(toConnect);
    };

    /* create a network, get bridge's IP on that network, to serve as DNS resolver */
    const preConfig = async function(sla) {
        let networkName = sla.localContainerName + "-bridge";
        let net = await createNetwork(networkName);

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
            });
    };

    const createNetwork = async function(name) {
        let config = {
            "Name": name,
            "Driver": "overlay",
            "Internal": true,
            "Attachable": true,
            "CheckDuplicate": true
        };

        return docker.createNetwork(config)
            .catch((err) => {
                console.log("[ERROR] creating network", name, err);
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
                    console.log("[connectFor] " + name + "DONE");
                    resolve(body);
                });
        });
    };

    module.preConfig        = preConfig;
    module.connectEndpoints = connectEndpoints;
    return module;
};