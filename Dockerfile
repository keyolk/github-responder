FROM golang:1.11.4-alpine@sha256:0e582bd4c47c5ecf6a1979c83c144b4d3172c8fb7901bde79cda128f33783083 AS build

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION
ARG CODEOWNERS

RUN apk add --no-cache \
    make \
    git \
    upx=3.94-r0

RUN mkdir -p /go/src/github.com/hairyhenderson/github-responder
WORKDIR /go/src/github.com/hairyhenderson/github-responder
COPY . /go/src/github.com/hairyhenderson/github-responder

RUN make build-x compress-all

FROM scratch AS artifacts

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /go/src/github.com/hairyhenderson/github-responder/bin/* /bin/

CMD [ "/bin/github-responder_linux-amd64" ]

FROM scratch AS github-responder

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION
ARG CODEOWNERS
ARG OS=linux
ARG ARCH=amd64

LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.title=github-responder \
      org.opencontainers.image.authors=$CODEOWNERS \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.source="https://github.com/hairyhenderson/github-responder"

COPY --from=artifacts /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=artifacts /bin/github-responder_${OS}-${ARCH} /github-responder

ENTRYPOINT [ "/github-responder" ]

FROM alpine:3.8@sha256:6e6778d41552b2d73b437e3e07c8e8299bd6903e9560419b1dd19e7a590fd670 AS github-responder-alpine

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION
ARG CODEOWNERS
ARG OS=linux
ARG ARCH=amd64

LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.title=github-responder \
      org.opencontainers.image.authors=$CODEOWNERS \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.source="https://github.com/hairyhenderson/github-responder"

RUN apk add --no-cache ca-certificates
COPY --from=artifacts /bin/github-responder_${OS}-${ARCH}-slim /bin/github-responder

ENTRYPOINT [ "/bin/github-responder" ]

FROM scratch AS github-responder-slim

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION
ARG CODEOWNERS
ARG OS=linux
ARG ARCH=amd64

LABEL org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.title=github-responder \
      org.opencontainers.image.authors=$CODEOWNERS \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.source="https://github.com/hairyhenderson/github-responder"

COPY --from=artifacts /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=artifacts /bin/github-responder_${OS}-${ARCH}-slim /github-responder

ENTRYPOINT [ "/github-responder" ]
