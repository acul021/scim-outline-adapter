FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /app ./application

FROM alpine:latest@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS base

RUN apk add -U --no-cache ca-certificates
RUN apk add -U --no-cache tzdata

ENV USER=appuser
ENV UID=10001

RUN adduser \
    --disabled-password \
    --gecos "" \
    --home "/nonexistent" \
    --shell "/sbin/nologin" \
    --no-create-home \
    --uid "${UID}" \
    "${USER}"

# Home of the optional USER_EXTERNAL_ID_FILE. Named volumes mounted here inherit
# this ownership, so the non-root user can write to them.
RUN mkdir /data && chown "${UID}:${UID}" /data

FROM scratch

COPY --from=base /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=base /etc/passwd /etc/passwd
COPY --from=base /etc/group /etc/group
COPY --from=base --chown=10001:10001 /data /data

ENV TZ=Europe/Berlin

USER appuser:appuser

EXPOSE 8080

COPY --from=build /app /app

ENTRYPOINT ["/app"]
