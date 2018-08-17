FROM golang:1.10.3-alpine3.8 as gobuild
WORKDIR /
ENV GOPATH="/go"
RUN apk update && apk add pkgconfig build-base bash autoconf automake libtool gettext openrc git libzmq zeromq-dev mercurial
#COPY . . if you update the libs below build with --no-cache
RUN go get -d github.com/gorilla/mux
RUN go get -d github.com/gorilla/websocket
RUN go get -d github.com/pebbe/zmq4
RUN go get -d github.com/docker/docker/client
RUN go get -d github.com/docker/docker/api/types
RUN go get -d github.com/pkg/errors
RUN go get -d github.com/skip2/go-qrcode
RUN go get -d github.com/docker/go-connections
RUN go get -d github.com/me-box/lib-go-databox
RUN rm -rf /go/src/github.com/docker/docker/vendor/github.com/docker/go-connections
RUN go get -d golang.org/x/net/proxy

COPY . .
RUN addgroup -S databox && adduser -S -g databox databox
RUN GGO_ENABLED=0 GOOS=linux go build -a -tags netgo -installsuffix netgo -ldflags '-s -w' -o app /*.go

FROM amd64/alpine:3.8
COPY --from=gobuild /etc/passwd /etc/passwd
RUN apk update && apk add libzmq
#TODO security
USER root
#TODO security
WORKDIR /
COPY --from=gobuild /app .
COPY --from=gobuild /www /www
LABEL databox.type="container-manager"
EXPOSE 80 443
RUN rm -rf /certs/*
CMD ["./app"]
#CMD ["sleep","9999999"]