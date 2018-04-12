/*jshint esversion: 6 */

const Config = require('./config.json');
const authToken = require('../certs/container-mananager-auth.json');

const fs = require('fs');
const http = require('http');
const https = require('https');
const express = require('express');
const bodyParser = require('body-parser');
const cookieParser = require('cookie-parser');
const databoxRequestPromise = require('./lib/databox-request-promise.js');
const url = require('url');
const databox = require('node-databox');
const crypto = require('crypto');
const cors = require('cors');
let sessionToken;

module.exports = {
	proxies: {},
	launch: function (conman) {
		function authError(res) {
			res.status(401).send("Authorization Required");
		}

		function verifyCookie(req) {
			return sessionToken && req.cookies.session === sessionToken;
		}

		function verifyToken(req) {
			// Grab the "Authorization" header.
			const auth = req.get("authorization");
			if (auth != null && auth.indexOf('Token ') === 0) {
				const token = auth.substr(6);
				if (token === authToken.token) {
					return true;
				}
			}

			return false;
		}

		function requestCatalogue(href) {
			if (href.includes('tcp://')) {
				//read /cat from core-store
				return new Promise((resolve) => {
					const kvc = databox.NewKeyValueClient(href, false);
					kvc.GetDatasourceCatalogue()
						.then((catStr) => {
							kvc.zestClient.ZMQsoc.close();
							console.log(catStr);
							resolve(JSON.parse(catStr));
						})
						.catch(() => {
							kvc.zestClient.ZMQsoc.close();
							console.log("Error /api/datasource/list can't get from " + href);
							resolve({});
						});
				});
			} else {
				//read /cat from store-json or other store over https
				return new Promise((resolve) => {
					console.log("Read from " + href);
					databoxRequestPromise({uri: href + '/cat'})
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
				});
			}
		}

		//Always proxy to the local store, app UI deals with remote stores
		this.proxies.store = Config.storeUrl_dev;

		const appHttp = express();
		appHttp.use(express.static('src/www/http'));
		appHttp.get('/cert.pem', (req, res) => {
			res.contentType('application/x-pem-file');
			res.sendFile('/certs/rootCert.crt');
		});
		appHttp.get('/cert.der', (req, res) => {
			res.contentType('application/x-x509-ca-cert');
			res.sendFile('/certs/rootCert.der');
		});
		const serverHttp = http.createServer(appHttp);
		serverHttp.listen(Config.portHttp);


		const appHttps = express();
		appHttps.enable('trust proxy');
		appHttps.use(express.static('src/www/https'));
		appHttps.use(express.static('src/www/http'));
		appHttps.use(cors());
		appHttps.use(cookieParser());
		appHttps.use((req, res, next) => {
			if (!verifyToken(req) && !verifyCookie(req)) {
				authError(res);
				return;
			}
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
									} else {
										next();
									}
								})
								.pipe(res)
								.on('error', (e) => {
									console.log('[Proxy] ERROR: ' + req.url + " " + e.message);
									next();
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

		appHttps.get('/api/store/cat/:app', (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}

			requestCatalogue('tcp://' + req.params.app + ':5555')
				.then((json) => {
					res.json(json);
				});
		});

		appHttps.get('/api/connect', (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}
			if (!sessionToken) {
				sessionToken = crypto.randomBytes(24).toString('base64');
			}
			res.cookie('session', sessionToken);
			res.send("connected");
		});

		appHttps.get('/api/qrcode.png', (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}
			res.contentType('application/png');
			res.sendFile('/certs/qrcode.png');
		});

		appHttps.get('/api/datasource/list', (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}

			requestCatalogue('https://arbiter:8080')
				.then((json) => {
					if ('items' in json) {
						const promises = [];
						for (const item of json.items) {
							promises.push(requestCatalogue(item.href));
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

								res.json(datasources);
							})
							.catch((error) => {
								console.log(error);
								res.json([]);
							});
					}
				});
		});

		appHttps.get('/api/installed/list', (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}
			conman.listServices()
				.then((services) => {
					console.log(services);
					let results = [];
					for (const service of services) {
						const name = service.Spec.Name;
						results.push(name);
					}

					console.log(results);
					res.json(results);
				})
				.catch((error) => {
					console.log(error);
					res.json(error);
				});
		});

		appHttps.get('/api/:type/list', (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
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
									for (const task of tasks) {
										let time = new Date(task.UpdatedAt);
										if (time > lastestTime) {
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
					res.cookie('session', sessionToken);
					res.json(tasks);
				})
				.catch((error) => {
					console.log(error);
					res.json(error);
				});
		});

		const jsonParser = bodyParser.json();
		appHttps.post('/api/install', jsonParser, (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
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

					res.json({status: 200, msg: "Success"});
				})
				.catch((error) => {
					console.log(error);
				});
		});

		appHttps.post('/api/restart', jsonParser, (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}
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


		appHttps.post('/api/uninstall', jsonParser, (req, res) => {
			if (!verifyToken(req)) {
				authError(res);
				return;
			}
			//console.log("Uninstalling " + req.body.id);
			const name = req.body.id;
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
