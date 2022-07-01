FROM alpine:3.15
EXPOSE 9953

RUN apk add --no-cache ca-certificates
RUN apk upgrade --update-cache --available
RUN adduser --disabled-password --no-create-home --uid=1983 spacelift
COPY dist/spacelift-promex /usr/bin/spacelift-promex

CMD ["/usr/bin/spacelift-promex", "serve"]
