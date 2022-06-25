FROM golang:1.18 as builder
ARG GIT_COMMIT
# RUN apt-get update && apt-get install -y inotify-tools postgresql-client
ARG DIR=/project
WORKDIR $DIR
ADD go.* $DIR/
RUN go mod download
ADD . $DIR
RUN CGO_ENABLED=0 go build -buildvcs=false -a -tags netgo -ldflags "-w -extldflags '-static' -X main.gitCommit=$GIT_COMMIT" -o /spacelift ./
# WORKDIR /go
# RUN CGO_ENABLED=0 go install -tags netgo -ldflags '-w -extldflags "-static"' github.com/go-delve/delve/cmd/dlv@latest
# RUN cp /go/bin/dlv /dlv
# WORKDIR $DIR

FROM alpine:3.15
EXPOSE 8080

RUN apk add --no-cache ca-certificates
RUN apk upgrade --update-cache --available
RUN adduser --disabled-password --no-create-home --uid=1983 spacelift
COPY --from=builder /spacelift /usr/bin/spacelift-promex

CMD ["/usr/bin/spacelift-promex", "serve"]
