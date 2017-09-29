const DATABOX_BRIDGE = "bridge";
const DATABOX_BRIDGE_ENDPOINT = "https://bridge:8080";

module.exports = function(docker) {
    var module = {};

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

    module.preConfig = preConfig;
    return module;
};