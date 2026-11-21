# Local build: docker build -t kropka .
# Releases use Dockerfile.release with the binary built by GoReleaser.
FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /kropka ./cmd/kropka

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /kropka /kropka
ENV KROPKA_BIND=0.0.0.0
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/kropka"]
CMD ["/data"]
