/*jshint esversion: 6 */

const conman = require('./container-manager.js');
const httpsHelper = require('./include/container-manger-https-helper');
const authToken = require('../certs/container-mananager-auth.json');

let containerMangerUIServer = null;

httpsHelper.init()
	.then(() => {
		conman.setHttpsHelper(httpsHelper);
		return conman.connect();
	})
	.then(()=>{
		return conman.reloadInstalledComponents();
	})
	.then((slas) => {
		//require here so env vars are set!
		containerMangerUIServer = require('./server.js');
		//set up the arbiter proxy
		containerMangerUIServer.proxies['arbiter'] = 'arbiter:8080';

		slas.forEach(sla => {
			//set up the proxy
			containerMangerUIServer.proxies[sla.name] = sla.name + ':8080';
		});

		console.log("Starting UI Server!!");
		console.log("Password = " + authToken.token);
		return containerMangerUIServer.launch(conman);
	})
	.catch(err => {
		console.log(err);
		const stack = new Error().stack;
		console.log(stack);
	});
