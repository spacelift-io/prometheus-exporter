FROM alpine:3.21
EXPOSE 9953

ARG TARGETARCH
ARG TARGETOS

RUN apk add --no-cache ca-certificates
RUN apk upgrade --update-cache --available
RUN adduser --disabled-password --no-create-home --uid=1983 spacelift

# The reason we're using a wildcard on the copy is that goreleaser sets a _v1 suffix for the
# amd64 target, which isn't included in the docker TARGETARCH variable
COPY dist/spacelift-promex_${TARGETOS}_${TARGETARCH}*/spacelift-promex /usr/bin/spacelift-promex
RUN chmod +x /usr/bin/spacelift-promex

CMD ["/usr/bin/spacelift-promex", "serve"]
