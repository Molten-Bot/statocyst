FROM golang:1.22-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/statocystd ./cmd/statocystd

FROM gcr.io/distroless/static:nonroot

WORKDIR /app
COPY --from=build /out/statocystd /usr/local/bin/statocystd

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/statocystd"]
