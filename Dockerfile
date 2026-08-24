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
	&& CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/smoke ./cmd/smoke \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/web ./cmd/web \
    && mkdir -p /out/tmp && chmod 1777 /out/tmp

FROM alpine:3.22
RUN apk add --no-cache ca-certificates poppler-utils
COPY --from=build /out/api /out/worker /out/migrate /out/healthcheck /out/eval /out/smoke /out/web /usr/local/bin/
COPY --from=build /src/eval/datasets /app/eval/datasets
COPY --from=build /src/demo /app/demo
COPY --from=build /src/web/dist /app/web
COPY --from=build --chmod=1777 /out/tmp /tmp
USER 65532:65532
EXPOSE 8080 8081
CMD ["/usr/local/bin/api"]
