FROM golang:1.22 as build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X github.com/gurgenyegoryan/kube-ops-copilot/internal/cli.Version=${VERSION} -X github.com/gurgenyegoryan/kube-ops-copilot/internal/cli.Commit=${COMMIT} -X github.com/gurgenyegoryan/kube-ops-copilot/internal/cli.Date=${DATE}" \
  -o /out/kube-ops-copilot ./cmd/kube-ops-copilot

FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/kube-ops-copilot /kube-ops-copilot

USER 65532:65532
ENTRYPOINT ["/kube-ops-copilot"]
