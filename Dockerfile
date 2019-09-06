
FROM quay.io/vxlabs/dep as deps
RUN mkdir -p $GOPATH/src/github.com/vx-labs
WORKDIR $GOPATH/src/github.com/vx-labs/nomad-quay-deployer
COPY Gopkg* ./
RUN dep ensure -vendor-only

FROM quay.io/vxlabs/dep as builder
RUN mkdir -p $GOPATH/src/github.com/vx-labs
WORKDIR $GOPATH/src/github.com/vx-labs/nomad-quay-deployer
COPY --from=deps $GOPATH/src/github.com/vx-labs/nomad-quay-deployer/vendor/ ./vendor/
COPY . ./
RUN go test ./...

FROM builder as binary-builder
RUN apk -U add git
RUN go build -buildmode=exe -ldflags="-s -w" -a -o /bin/server .

FROM alpine as prod
ENTRYPOINT ["/usr/bin/server"]
RUN apk -U add ca-certificates && \
    rm -rf /var/cache/apk/*
COPY --from=binary-builder /bin/server /usr/bin/server
