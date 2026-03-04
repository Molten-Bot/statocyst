FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /out/statocystd ./cmd/statocystd

FROM gcr.io/distroless/static:nonroot

WORKDIR /app
COPY --from=build /out/statocystd /usr/local/bin/statocystd

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/statocystd"]
