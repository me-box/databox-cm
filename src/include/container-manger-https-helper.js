/*jshint esversion: 6 */

const forge = require('node-forge');
const fs = require('fs');

const attrs = [
	{name: 'commonName', value: 'Databox'},
	{name: 'organizationName', value: 'University of Nottingham'},
	{name: 'countryName', value: 'UK'},
	{shortName: 'ST', value: 'Nottinghamshire'},
	{name: 'localityName', value: 'Nottingham'},
	{shortName: 'OU', value: 'Mixed Reality Lab'}
];

let rootPems;

const devCertPath = '/run/secrets/DATABOX_CM.pem';

//Generate the CM root cert at startup.
//If in DEV mode we need to use the same certs at restart because the docker demon has to trust the container manger CA to verify
//the local registry. If we are not in dev mode then the certs are generated at each restart of the container manger.
const init = function () {
	return new Promise((resolve, reject) => {
		fs.readFile(devCertPath, function (err, data) {
			if (err === null) {
				rootPems = data;
				resolve();
			} else {
				reject("[ERROR]" + devCertPath + " not found");
			}
		});
	});
};

//based on code extracted from the selfsigned module Licence MIT
const createClientCert = async function (commonName) {

	function toPositiveHex(hexString) {
		let mostSiginficativeHexAsInt = parseInt(hexString[0], 16);
		if (mostSiginficativeHexAsInt < 8) {
			return hexString;
		}

		mostSiginficativeHexAsInt -= 8;
		return mostSiginficativeHexAsInt.toString() + hexString.substring(1);
	}

	return new Promise(async (resolve, reject) => {
		let pki = forge.pki;
		let pem = {};

		let clientkeys = forge.pki.rsa.generateKeyPair(2048);
		let clientcert = forge.pki.createCertificate();
		clientcert.serialNumber = toPositiveHex(forge.util.bytesToHex(forge.random.getBytesSync(9)));
		clientcert.validity.notBefore = new Date();
		clientcert.validity.notAfter = new Date();
		clientcert.validity.notAfter.setFullYear(clientcert.validity.notBefore.getFullYear() + 10);

		const clientAttrs = [
			{name: 'commonName', value: commonName},
			{name: 'organizationName', value: 'University of Nottingham'},
			{name: 'countryName', value: 'UK'},
			{shortName: 'ST', value: 'Nottinghamshire'},
			{name: 'localityName', value: 'Nottingham'},
			{shortName: 'OU', value: 'Mixed Reality Lab'}
		];

		clientcert.setSubject(clientAttrs);
		// Set the issuer to the parent key
		clientcert.setIssuer(attrs);

		clientcert.setExtensions([{
			name: 'basicConstraints',
			cA: true
		}, {
			name: 'keyUsage',
			keyCertSign: true,
			digitalSignature: true,
			nonRepudiation: true,
			keyEncipherment: true,
			dataEncipherment: true
		}, {
			name: 'subjectAltName',
			altNames: [
				{
					type: 2, // DNS name
					value: commonName
				},
				{
					type: 2, // DNS name
					value: 'localhost'
				}
			]
		}]);

		clientcert.publicKey = clientkeys.publicKey;

		// Sign client cert with root cert
		try {
			clientcert.sign(pki.privateKeyFromPem(rootPems));
		} catch (e) {
			reject("ERROR", e);
		}
		pem.clientprivate = forge.pki.privateKeyToPem(clientkeys.privateKey);
		pem.clientpublic = forge.pki.publicKeyToPem(clientkeys.publicKey);
		pem.clientcert = forge.pki.certificateToPem(clientcert);
		resolve(JSON.stringify(pem));
	});
};

module.exports = {init: init, createClientCert: createClientCert};
