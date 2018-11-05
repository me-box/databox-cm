package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"testing"

	libDatabox "github.com/me-box/lib-go-databox"
)

func BenchmarkGenerateKeyRSA2024(b *testing.B) {
	// write b.N times
	for n := 0; n < b.N; n++ {
		rsa.GenerateKey(rand.Reader, 2048)
	}
}

func BenchmarkGenerateKeyEllipticP256(b *testing.B) {
	// write b.N times
	for n := 0; n < b.N; n++ {
		ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
}

func BenchmarkGenCert(b *testing.B) {
	// write b.N times
	for n := 0; n < b.N; n++ {
		GenCert("./certs/containerManager.crt", "test_cert", []string{"127.0.0.1"}, []string{"localhost"})
	}
}

func BenchmarkGenerateArbiterToken(b *testing.B) {
	// write b.N times
	for n := 0; n < b.N; n++ {
		GenerateArbiterToken()
	}
}

func BenchmarkImageExists(b *testing.B) {

	cm := NewContainerManager("", "", "", &libDatabox.ContainerManagerOptions{})
	// write b.N times
	for n := 0; n < b.N; n++ {
		cm.imageExists("tosh/missingTestImage:latest")
	}
}
