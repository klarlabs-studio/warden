# Build outside the hook. The resulting image identity, not this recipe alone,
# must be reviewed and pinned in execution requirements.
FROM docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514

ENV GOTOOLCHAIN=local CGO_ENABLED=1 GOPATH=/opt/go GOMODCACHE=/opt/go/pkg/mod
ENV PATH=/opt/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
RUN go install go.klarlabs.de/coverctl@v1.20.0

WORKDIR /opt/warden-dependencies
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY .github/warden/linux-test/run.sh /usr/local/bin/warden-linux-test
RUN chmod 0755 /usr/local/bin/warden-linux-test

ENV GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly
WORKDIR /workspace
