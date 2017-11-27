/*jshint esversion: 6 */

const Config = require('./config.json');
//setup dev env
const DATABOX_DEV = process.env.DATABOX_DEV === '1';


const http = require('http');
const https = require('https');
const express = require('express');
const bodyParser = require('body-parser');
const request = require('request');
const databoxRequestPromise = require('./lib/databox-request-promise.js');
const databoxAgent = require('./lib/databox-https-agent.js');
const url = require('url');
const databox = require('node-databox');

const app = express();

module.exports = {
	proxies: {},
	app: app,
	launch: function (port, conman, httpsHelper) {

		const server = http.createServer(app);
		const installingApps = {};

		//Always proxy to the local store, app UI deals with remote stores
		this.proxies.store = Config.storeUrl_dev;


		app.enable('trust proxy');
		app.set('views', 'src/www');
		app.set('view engine', 'pug');
		app.use(express.static('src/www'));

		app.use((req, res, next) => {
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

		// Needs to be after the proxy
		app.use(bodyParser.json());
		app.use(bodyParser.urlencoded({extended: false}));

		app.get('/api/datasource/list', (req, res) => {
			databoxRequestPromise({uri: 'https://arbiter:8080/cat'})
				.then((request) => {
					console.log(JSON.stringify(request));
					let body = [];
					request
						.on('error', (error) => {
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
											databoxRequestPromise({uri: item.href + '/cat'})
												.then((request) => {
													let body = [];
													request
														.on('error', (error) => {
															resolve({});
														})
														.on('data', (chunk) => {
															body.push(chunk);
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
										let datasources = [];
										for (const result of results) {
											if ('items' in result) {
												for (const item of result.items) {
													datasources.push(item);
													// if ('item-metadata' in item) {
													// 	for(const metadataItem of item['item-metadata']) {
													// 		if(metadataItem.rel === '') {
													// 			datasources.push(item);
													// 		}
													// 	}
													// }
												}
											}
										}

										res.header('Access-Control-Allow-Origin', '*');
										res.header('Access-Control-Allow-Credentials', true);
										res.json(datasources);
									})
									.catch(function (error) {
										console.log(error);
										res.header('Access-Control-Allow-Origin', '*');
										res.header('Access-Control-Allow-Credentials', true);
										res.json([]);
									});
							}
						});
				});
		});

		app.get('/api/installed/list', (req, res) => {
			conman.listContainers()
				.then((containers) => {
					let results = [];
					for (const container of containers) {
						const name = container.Labels['com.docker.swarm.service.name'];
						if (results.indexOf(name) === -1) {
							results.push(name);
						}
					}

					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					res.json(results);
				});

		});

		app.get('/api/:type/list', (req, res) => {
			conman.listContainers()
				.then((containers) => {
					let results = {};
					for (const container of containers) {
						if (req.params.type === 'all'
							|| container.Labels['databox.type'] === req.params.type
							|| (req.params.type === 'system'
								&& container.Labels['databox.type'] !== 'app'
								&& container.Labels['databox.type'] !== 'driver'
								&& container.Labels['databox.type'] !== 'store')) {
							const name = container.Labels['com.docker.swarm.service.name'];
							if (results.hasOwnProperty(name)) {
								const existing = results[name];
								if (existing.Created < container.Created) {
									results[name] = container;
								}
							}
							else {
								results[name] = container;
							}

						}
					}

					res.header('Access-Control-Allow-Origin', '*');
					res.header('Access-Control-Allow-Credentials', true);
					res.json(Object.keys(results).map(key => results[key]));
				});
		});

		app.get('/list-apps', (req, res) => {
			let names = [];
			let result = [];

			conman.listContainers()
				.then((containers) => {
					for (let container of containers) {
						let name = container.Names[0].substr(1).split('.')[0];
						names.push(name);
						result.push({
							name: name,
							container_id: container.Id,
							type: container.Labels['databox.type'] === undefined ? 'app' : container.Labels['databox.type'],
							status: container.State
						});
					}

					for (let installingApp in installingApps) {
						if (names.indexOf(installingApp) === -1) {
							names.push(installingApp);
							result.push({
								name: installingApp,
								type: installingApps[installingApp],
								status: 'installing'
							});
						}
					}

					const options = {'url': '', 'method': 'GET'};

					//Always use local store, app UI deals with remote stores
					options.url = Config.storeUrl_dev + '/app/list';

					return new Promise((resolve, reject) => {
						request(options, (error, response, body) => {
							if (error) {
								console.log("Error: " + options.url);
								reject(error);
								return;
							}

							resolve(JSON.parse(body).apps);
						});

					});
				})
				.then((apps) => {
					for (let app of apps) {
						if (names.indexOf(app.manifest.name) === -1) {
							names.push(app.manifest.name);
							result.push({
								name: app.manifest.name,
								type: app.manifest['databox-type'] === undefined ? 'app' : app.manifest['databox-type'],
								status: 'uninstalled',
								author: app.manifest.author
							});
						}
					}

					res.json(result);
				})
				.catch((err) => {
					console.log("[Error] ", err);
					res.json(err);
				});

		});

		app.options('/api/install', (req, res) => {
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Methods', 'POST');
			res.header('Access-Control-Allow-Headers', 'Content-Type');
			res.json({status: 200, msg: "Success"});
		});

		const jsonParser = bodyParser.json();
		app.post('/api/install', jsonParser, (req, res) => {
			const sla = req.body;
			console.log(sla);
			installingApps[sla.name] = sla['databox-type'] === undefined ? 'app' : sla['databox-type'];

			conman.install(sla)
				.then((config) => {
					console.log('[' + sla.name + '] Installed', config);
					for (const name of config) {
						delete installingApps[name];
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

		app.options('/api/restart', (req, res) => {
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Methods', 'POST');
			res.header('Access-Control-Allow-Headers', 'Content-Type');
			res.json({status: 200, msg: "Success"});
		});

		app.post('/api/restart', (req, res) => {
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


		app.options('/api/uninstall', (req, res) => {
			res.header('Access-Control-Allow-Origin', '*');
			res.header('Access-Control-Allow-Credentials', true);
			res.header('Access-Control-Allow-Methods', 'POST');
			res.header('Access-Control-Allow-Headers', 'Content-Type');
			res.json({status: 200, msg: "Success"});
		});

		app.post('/api/uninstall', (req, res) => {
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

		server.listen(port);
	}
};
