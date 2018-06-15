PACKAGE  = databox
DATABOX_GOPATH="$(shell echo ~/go):$(shell pwd):$(shell echo ${GOPATH})"
.PHONY: all
all: build

.PHONY: build
build:
	docker build -t dev/container-manager .


.PHONY: build-no-cache
build-no-cache:
	docker build -t dev/container-manager . --no-cache

.PHONY: test
test:
	#does it build is the best we can do here fror now
	docker build -t dev/container-manager . --no-cache