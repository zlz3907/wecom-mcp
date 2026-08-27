FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY teamserver/go.mod teamserver/go.sum ./teamserver/
RUN cd teamserver && go mod download

COPY internal ./internal
COPY teamserver/cmd ./teamserver/cmd
COPY teamserver/internal ./teamserver/internal
RUN cd teamserver && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wecom-mcp-team ./cmd/wecom-mcp-team

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/wecom-mcp-team /usr/local/bin/wecom-mcp-team
EXPOSE 17801
ENTRYPOINT ["/usr/local/bin/wecom-mcp-team"]
CMD ["--config", "/var/lib/wecom-mcp/config/instance.json", "--listen", "0.0.0.0:17801"]
