/*jshint esversion: 6 */

const Config = require('./config.json');
const authToken = require('../certs/container-mananager-auth.json');

const fs = require('fs');
const http = require('http');
const https = require('https');
const express = require('express');
const bodyParser = require('body-parser');
const databoxRequestPromise = require('./lib/databox-request-promise.js');
const url = require('url');
const databox = require('node-databox');

module.exports = {
	proxies: {},
	launch: function (conman) {
		function verifyToken(req, res) {
			// Grab the "Authorization" header.
			const auth = req.get("authorization");
			if (auth != null && auth.indexOf('Token ') === 0) {
				const token = auth.substr(6);
				if(token === authToken.token) {
					return true;
				}
			}
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.status(401).send("Authorization Required");
			return false;
		}

		//Always proxy to the local store, app UI deals with remote stores
		this.proxies.store = Config.storeUrl_dev;

		const appHttp = express();
		appHttp.use(express.static('src/www/http'));
		appHttp.get('/cert.pem', (req, res) => {
			res.contentType('application/x-pem-file');
			res.sendFile('/certs/containerManager.crt');
		});
		const serverHttp = http.createServer(appHttp);
		serverHttp.listen(Config.portHttp);


		const appHttps = express();
		appHttps.enable('trust proxy');
		appHttps.use(express.static('src/www/https'));
		appHttps.use(express.static('src/www/http'));

		appHttps.use((req, res, next) => {
			const firstPart = req.path.split('/')[1];
			if (firstPart in this.proxies) {
				const replacement = this.proxies[firstPart];
				let proxyURL;
				if (replacement.indexOf('://') !== -1) {
					const parts = url.parse(replacement);
					parts.pathname = req.baseUrl + req.path.substring(firstPart.length + 1);
					parts.query = req.query;
					proxyURL = url.format(parts);
				}
				else {
					proxyURL = url.format({
						protocol: 'https',
						host: replacement,
						pathname: req.baseUrl + req.path.substring(firstPart.length + 1),
						query: req.query
					});
				}

				console.log("[Proxy] " + req.method + ": " + req.url + " => " + proxyURL);
				let retried = false;
				let retryOnce = function () {
					databoxRequestPromise({uri: proxyURL})
						.then((resolvedRequest) => {

							return req.pipe(resolvedRequest)
								.on('error', (e) => {
									console.log('[Proxy] ERROR: ' + req.url + " " + e.message);
									if (!retried && e.message.includes("getaddrinfo ENOTFOUND")) {
										retried = true;
										console.log('[Proxy] retry ' + req.url);
										retryOnce();
									}
								})
								.pipe(res)
								.on('error', (e) => {
									console.log('[Proxy] ERROR: ' + req.url + " " + e.message);
								})
								.on('end', () => {
									next();
								});
						});
				};
				retryOnce();
			} else {
				next();
			}
		});

		appHttps.get('/api/qrcode.png', (req, res) => {
			if(!verifyToken(req, res)) {
				return;
			}
			res.contentType('application/png');
			res.sendFile('/certs/qrcode.png');
		});

		// Needs to be after the proxy
		appHttps.use(bodyParser.json());
		appHttps.use(bodyParser.urlencoded({extended: false}));

		appHttps.get('/api/datasource/list', (req, res) => {
			if(!verifyToken(req, res)) {
				return
			}
			databoxRequestPromise({uri: 'https://arbiter:8080/cat'})
				.then((request) => {
					console.log(JSON.stringify(request));
					let body = [];
					request
						.on('error', () => {
							res.header('Access-Control-Allow-Origin', '*');
							res.header('Access-Control-Allow-Credentials', true);
							res.json([]);
						})
						.on('data', (chunk) => {
							body.push(chunk);
						})
						.on('end', () => {
							const json = JSON.parse(Buffer.concat(body).toString());
							if ('items' in json) {
								const promises = [];
								for (const item of json.items) {
									promises.push(new Promise((resolve, reject) => {
										if(item.href.includes('tcp://')) {
											//read /cat from core-store
											let kvc = databox.NewKeyValueClient(item.href,false);
											kvc.GetDatasourceCatalogue()
											.then((catStr)=>{
												kvc.zestClient.ZMQsoc.close();
												console.log(catStr);
												resolve(JSON.parse(catStr));
											})
											.catch(()=>{
												kvc.zestClient.ZMQsoc.close();
												console.log("Error /api/datasource/list can't get from " + item.href);
												resolve({});
											});
										} else {
											//read /cat from store-json or other store over https
											console.log("Read from " + item.href);
											databoxRequestPromise({uri: item.href + '/cat'})
												.then((request) => {
													console.log(request);
													let body = [];
													request
														.on('error', (error) => {
															console.log(error);
															resolve({});
														})
														.on('data', (chunk) => {
															body.push(chunk);
															console.log(Buffer.concat(body).toString());
														})
														.on('end', () => {
															resolve(JSON.parse(Buffer.concat(body).toString()));
														});
												});
											}
									}));
								}
								return Promise.all(promises)
									.then(results => {
										const datasources = [];
										for (const result of results) {
											if ('items' in result) {
												for (const item of result.items) {
													datasources.push(item);
												}
											}
										}

										res.header('Access-Control-Allow-Origin', '*');
										res.header('Access-Control-Allow-Credentials', true);
										res.json(datasources);
									})
									.catch((error) => {
										console.log(error);
										res.header('Access-Control-Allow-Origin', '*');
										res.header('Access-Control-Allow-Credentials', true);
										res.json([]);
									});
							}
						});
				});
		});

		appHttps.get('/api/installed/list', (req, res) => {
			if(!verifyToken(req, res)) {
				return
			}
			conman.listServices()
				.then((services) => {
					console.log(services);
					let results = [];
					for (const service of services) {
						const name = service.Spec.Name;
						results.push(name);
					}

					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					console.log(results);
					res.json(results);
				})
				.catch((error) => {
					console.log(error);
					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					res.json(error);
				});
		});

		appHttps.options('/api/:type/list', (req, res) => {
			console.log("");
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Headers', 'Authorization');
			res.header('Access-Control-Allow-Methods', 'GET');
			res.json({status: 200, msg: "Success"});
		});

		appHttps.get('/api/:type/list', (req, res) => {
			if(!verifyToken(req, res)) {
				return
			}
			conman.listServices(req.params.type)
				.then((services) => {
					let proms = [];
					for (const service of services) {
						const name = service.Spec.Name;
						proms.push(conman.listTasks(name)
							.then((tasks) => {
								let result = {
									name: name,
									type: service.Spec.Labels['databox.type'],
								};
								if (tasks.length > 0) {
									let lastestTask = tasks[0];
									let lastestTime = new Date(lastestTask.UpdatedAt);
									for(const task of tasks) {
										let time = new Date(task.UpdatedAt);
										if(time > lastestTime) {
											lastestTask = task;
											lastestTime = time;
										}
									}
									result.desiredState = lastestTask.DesiredState;
									result.state = lastestTask.Status.State;
									result.status = lastestTask.Status.Message;
								}
								return result;
							}));
					}

					return Promise.all(proms);
				})
				.then((tasks) => {
					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					res.json(tasks);
				})
				.catch((error) => {
					console.log(error);
					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					res.json(error);
				});
		});

		appHttps.options('/api/install', (req, res) => {
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Methods', 'POST');
			res.header('Access-Control-Allow-Headers', 'Authorization, Content-Type');
			res.json({status: 200, msg: "Success"});
		});

		const jsonParser = bodyParser.json();
		appHttps.post('/api/install', jsonParser, (req, res) => {
			if(!verifyToken(req, res)) {
				return
			}
			const sla = req.body;
			console.log(sla);

			conman.install(sla)
				.then((config) => {
					console.log('[' + sla.name + '] Installed', config);
					for (const name of config) {
						this.proxies[name] = name + ':8080';
						console.log("Proxy added for ", name)
					}

					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					res.json({status: 200, msg: "Success"});
				})
				.catch((error) => {
					console.log(error);
				});
		});

		appHttps.options('/api/restart', (req, res) => {
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Methods', 'POST');
			res.header('Access-Control-Allow-Headers', 'Authorization, Content-Type');
			res.json({status: 200, msg: "Success"});
		});

		appHttps.post('/api/restart', (req, res) => {
			if(!verifyToken(req, res)) {
				return
			}
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			conman.restart(req.body.id)
				.then(() => {
					res.json({status: 200, msg: "Success"});
				})
				.catch((err) => {
					console.log(err);
					res.status(500);
					res.json(err)
				});
		});


		appHttps.options('/api/uninstall', (req, res) => {
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Methods', 'POST');
			res.header('Access-Control-Allow-Headers', 'Authorization, Content-Type');
			res.json({status: 200, msg: "Success"});
		});

		appHttps.post('/api/uninstall', (req, res) => {
			if(!verifyToken(req, res)) {
				return
			}
			//console.log("Uninstalling " + req.body.id);
			const name = req.body.id;
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			conman.uninstall(name)
				.then(() => {
					console.log('[' + name + '] Uninstalled');
					res.json({"status": "success"});
				})
				.catch((err) => {
					console.log(err);
					res.status(500);
					res.json(err)
				});
		});

		const certificate = fs.readFileSync('/certs/container-manager.pem');
		const credentials = {key: certificate, cert: certificate};
		const serverHttps = https.createServer(credentials, appHttps);
		serverHttps.listen(Config.portHttps);
	}
};
