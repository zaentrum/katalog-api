# Base-image registry prefix. Empty default = public Docker Hub; a private
# deploy mirror passes --build-arg BASE=registry.example/library/ .
ARG BASE=
# syntax=docker/dockerfile:1.7
FROM ${BASE}golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/katalog-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/katalog-api /katalog-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/katalog-api"]
