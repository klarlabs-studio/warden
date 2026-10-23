# Build outside the hook. The resulting image identity, not this recipe alone,
# must be reviewed and pinned in execution requirements.
FROM docker.io/library/golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af

# The Go suite exercises Python reporting, shell/JQ actions and the npm wrapper.
# Record resolved distro versions; consumers pin the finished image identity.
RUN apt-get update && apt-get install -y --no-install-recommends python3 nodejs jq libnss-wrapper \
    && dpkg -L libnss-wrapper | awk '/\/libnss_wrapper.so$/ { print; exit }' > /opt/warden-nss-wrapper-path \
    && test -s /opt/warden-nss-wrapper-path \
    && dpkg-query -W > /opt/warden-distro-packages.txt \
    && rm -rf /var/lib/apt/lists/*

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
