# Multi-stage Go build of the github.com/fclairamb/ftpserverlib FTP server used by
# the ftp*.star E2E scenarios (a Go server, no Python runtime). The passive-data
# port is pinned (FTP_PASV_PORT, default 30000) so the scenario can publish it as a
# stable host port for the separate FTP data channel, and FTP_PASV_ADDRESS sets the
# PASV masquerade address. Active mode is left enabled, so this one image serves both
# the passive and the active-mode scenarios.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY ftpd.go ./
RUN CGO_ENABLED=0 go build -o /ftpd .

FROM alpine:3.20
COPY --from=build /ftpd /ftpd
ENTRYPOINT ["/ftpd"]
