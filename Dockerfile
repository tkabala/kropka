# Local build: docker build -t kropka .
# Releases use Dockerfile.release with the binary built by GoReleaser.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /kropka ./cmd/kropka

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /kropka /kropka
ENV KROPKA_BIND=0.0.0.0
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/kropka"]
CMD ["/data"]
