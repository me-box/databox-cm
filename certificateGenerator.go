package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	b64 "encoding/base64"
	"encoding/pem"
	"io/ioutil"
	"math/big"
	"net"
	"os"
	"time"

	libDatabox "github.com/toshbrown/lib-go-databox"
)

func GenCert(CAFilePath string, commonName string, ips []string, hostNames []string) []byte {

	libDatabox.Debug("[GenCert] " + commonName)

	rootCertPem, err := ioutil.ReadFile(CAFilePath)
	libDatabox.ChkErrFatal(err)

	rootCertBytes, rest := pem.Decode(rootCertPem)

	rootCert, err := x509.ParseCertificate(rootCertBytes.Bytes)
	libDatabox.ChkErrFatal(err)

	rootPrivateKeyBytes, _ := pem.Decode(rest)
	rootPrivateKey, err := x509.ParsePKCS1PrivateKey(rootPrivateKeyBytes.Bytes)
	libDatabox.ChkErrFatal(err)

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	libDatabox.ChkErrFatal(err)

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, snErr := rand.Int(rand.Reader, serialNumberLimit)
	libDatabox.ChkErrFatal(snErr)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"University of Nottingham"},
			Country:      []string{"UK"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		AuthorityKeyId:        rootCert.AuthorityKeyId,
		RawIssuer:             rootCert.RawIssuer,
	}
	for _, ip := range ips {
		template.IPAddresses = append(template.IPAddresses, net.ParseIP(ip))
	}
	for _, h := range hostNames {
		template.DNSNames = append(template.DNSNames, h)
	}

	template.IsCA = false

	derBytes, derErr := x509.CreateCertificate(rand.Reader, &template, rootCert, &priv.PublicKey, rootPrivateKey)
	libDatabox.ChkErrFatal(derErr)

	cert := new(bytes.Buffer)
	pem.Encode(cert, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	b := x509.MarshalPKCS1PrivateKey(priv)
	pem.Encode(cert, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: b})

	asn1Bytes, pubErr := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	libDatabox.ChkErrFatal(pubErr)

	pem.Encode(cert, &pem.Block{Type: "PUBLIC KEY", Bytes: asn1Bytes})

	return cert.Bytes()
}

func GenCertToFile(CAFilePath string, commonName string, ips []string, hostNames []string, outputFilePath string) {

	cert := GenCert(CAFilePath, commonName, ips, hostNames)

	certOut, err := os.Create(outputFilePath)
	libDatabox.ChkErrFatal(err)

	_, err = certOut.Write(cert)
	libDatabox.ChkErrFatal(err)

	certOut.Close()

}

func GenRootCA(CAFilePathPriv string, CAFilePathPub string) {
	libDatabox.Info("GenRootCA called")
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	libDatabox.ChkErrFatal(err)

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, snErr := rand.Int(rand.Reader, serialNumberLimit)
	libDatabox.ChkErrFatal(snErr)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "Databox",
			Organization: []string{"University of Nottingham"},
			Country:      []string{"UK"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	template.IsCA = true
	template.KeyUsage |= x509.KeyUsageCertSign

	derBytes, derErr := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	libDatabox.ChkErrFatal(derErr)

	certOutPub, err := os.Create(CAFilePathPub)
	libDatabox.ChkErrFatal(err)
	pem.Encode(certOutPub, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOutPub.Close()

	certOutPriv, err := os.Create(CAFilePathPriv)
	libDatabox.ChkErrFatal(err)
	pem.Encode(certOutPriv, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	b := x509.MarshalPKCS1PrivateKey(priv)
	pem.Encode(certOutPriv, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: b})
	certOutPriv.Close()

}

func GenerateArbiterToken() []byte {
	len := 32
	data := make([]byte, len)
	_, err := rand.Read(data)
	if err != nil {
		libDatabox.Err(err.Error())
		return []byte{}
	}

	return data

}

func GenerateArbiterTokenToFile(outputFilePath string) {
	libDatabox.Debug("GenerateArbiterTokenToFile" + outputFilePath)
	out, err := os.Create(outputFilePath)
	libDatabox.ChkErrFatal(err)

	data := GenerateArbiterToken()
	libDatabox.Debug("GenerateArbiterTokenToFile data=" + b64.StdEncoding.EncodeToString(data))

	out.WriteString(b64.StdEncoding.EncodeToString(data))
	out.Close()
}
