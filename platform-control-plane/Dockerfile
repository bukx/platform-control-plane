FROM golang:1.24.4 AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

RUN go build -o /out/platformd ./cmd/platformd
RUN go build -o /out/platformmigrate ./cmd/platformmigrate

FROM gcr.io/distroless/base-debian12

COPY --from=builder /out/platformd /platformd
COPY --from=builder /out/platformmigrate /platformmigrate

EXPOSE 8080

ENTRYPOINT ["/platformd"]
