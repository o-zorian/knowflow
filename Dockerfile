FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/eval ./cmd/eval \
    && mkdir -p /out/tmp && chmod 1777 /out/tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/api /out/worker /out/migrate /out/healthcheck /out/eval /usr/local/bin/
COPY --from=build /src/eval/datasets /app/eval/datasets
COPY --from=build --chmod=1777 /out/tmp /tmp
USER 65532:65532
EXPOSE 8080
CMD ["/usr/local/bin/api"]
