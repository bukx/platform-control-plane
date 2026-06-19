FROM golang:1.24.4 AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

RUN go build -o /out/platformd ./cmd/platformd
RUN go build -o /out/platformmigrate ./cmd/platformmigrate

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates git gh && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --system --uid 65532 --create-home appuser

COPY --from=builder /out/platformd /platformd
COPY --from=builder /out/platformmigrate /platformmigrate

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/platformd"]
