# ocp-toolkit — image de distribution.
# Contient ocptk + oc : utilisable depuis n'importe quel poste avec Docker/Podman,
# ou en Job/CronJob dans le cluster (voir deploy/k8s/).
#
#   docker build -t adon1996/ocp-toolkit:0.1.0 .
#   docker run --rm -it -v ~/.kube:/root/.kube -v $PWD/reports:/work/reports adon1996/ocp-toolkit:0.1.0 validate

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/ocptk ./cmd/ocptk

# oc récupéré depuis l'image officielle origin-cli (pas de téléchargement HTTP :
# compatible build derrière un miroir d'images).
FROM quay.io/openshift/origin-cli:latest AS occli

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
RUN microdnf install -y tar gzip && microdnf clean all
COPY --from=occli /usr/bin/oc /usr/bin/oc
COPY --from=build /out/ocptk /usr/bin/ocptk
WORKDIR /work
ENTRYPOINT ["ocptk"]
CMD ["--help"]
