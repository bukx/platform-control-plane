FROM golang:1.24.4 AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

RUN go build -o /out/platformd ./cmd/platformd

FROM gcr.io/distroless/base-debian12

COPY --from=builder /out/platformd /platformd

EXPOSE 8080

ENTRYPOINT ["/platformd"]
