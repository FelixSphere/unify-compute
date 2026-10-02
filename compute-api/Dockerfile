# Two stages: build with the toolchain, run from a distroless image with no
# shell, as a non-root user. The SKU catalogue ships inside the image on purpose
# (see AGENTS.md: a price change is a code change, not a config edit).
FROM golang:1.24-alpine AS build
ARG BUILD=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Build=${BUILD}" -o /out/compute-api ./cmd/compute-api

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/compute-api /app/compute-api
COPY config/skus.json /app/config/skus.json
COPY config/templates.json /app/config/templates.json
ENV LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/compute-api"]
