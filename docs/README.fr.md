# ocp-toolkit (`ocptk`)

Outil CLI d'assistance à la **migration de nœuds Kubernetes vanilla vers Red Hat
OpenShift 4.x à grande échelle (5000+ nœuds)**. Trois modules, pilotables par
menu interactif ou en sous-commandes pour la CI/CD.

```
=== OpenShift Migration Toolkit (ocptk) ===
1) Validation des nœuds migrés            (tests + rapport)
2) Provisioning cluster OpenShift 4.x     (génération config + install)
3) Migration d'applications K8s → OpenShift
q) Quitter
```

## Démarrage rapide (5 minutes, validé sur un cluster OpenShift réel)

```bash
# 1. Récupérer l'outil (l'image embarque oc — rien d'autre à installer)
docker pull adon1996/ocp-toolkit:latest

# 2. Se connecter au cluster (console OpenShift → user → "Copy login command")
mkdir -p .kube work
docker run --rm -v "$PWD/.kube:/root/.kube" --entrypoint oc adon1996/ocp-toolkit:latest \
  login --token=sha256~XXXX --server=https://api.<cluster>:6443

# 3. État des lieux sans risque (dry-run, lecture seule) + rapport HTML
docker run --rm -v "$PWD/.kube:/root/.kube" -v "$PWD/work:/work" \
  adon1996/ocp-toolkit:latest validate
# → work/reports/validate-<ts>/report.html

# 4. Exporter et transformer les applis d'un namespace en Kustomize GitOps
docker run --rm -v "$PWD/.kube:/root/.kube" -v "$PWD/work:/work" \
  adon1996/ocp-toolkit:latest migrate --from-cluster --namespaces mon-app \
  --fix-scc --fix-nodeport --confirm
# → work/generated/migrate/<projet>/<app>/base/ + README par appli

# 5. Déployer les manifestes corrigés
oc apply -k work/generated/migrate/<projet>/<app>/base
```

**Démo réalisée sur Red Hat Developer Sandbox** : une appli legacy avec
`runAsUser` en dur est rejetée par la SCC (`unable to validate against any
security context constraint`) → `ocptk migrate --fix-scc` la transforme →
`oc apply -k` → **1/1 READY + Route créée**. Le module validate a couvert les
11 nœuds du cluster via son DaemonSet (SCC restricted-v2, SELinux, DNS,
registre interne : PASS), même sans droit `cluster-reader`.

## Pourquoi Go (et pas Python)

Le cahier des charges initial visait Python ; l'implémentation est en **Go**,
à périmètre fonctionnel identique, pour des raisons opérationnelles :

| Critère | Go | Python |
|---|---|---|
| Déploiement chez le client | **1 binaire statique** (`scp` et c'est fini) | Python 3.11 + venv + pip install (souvent interdit en air-gap) |
| Air-gapped | binaire ou image OCI auto-suffisants | miroir PyPI à monter |
| Image conteneur | ~30 Mo (ubi-minimal + binaire) | 150-400 Mo |
| Parallélisme (collecte 5000 nœuds) | goroutines natives | threads/asyncio plus lourds |
| Écosystème cible | kubectl, oc, openshift-install, ArgoCD… sont en Go | — |

Le contrat de la spec est respecté à l'identique : commande `ocptk`, menu à 3
modules, `--dry-run` par défaut / `--confirm` pour exécuter, idempotence
(`apply` + label `app.kubernetes.io/managed-by: ocp-toolkit`), rapports
horodatés JSON + HTML + JUnit, option `--registry-mirror` partout, logs
structurés dans `./logs/`.

## Installation

**Binaire** (poste opérateur ou bastion) :

```bash
make build            # bin/ocptk pour l'OS courant
make release          # linux/amd64+arm64, windows, darwin
```

**Docker / Podman** (aucune installation sur le poste — l'image embarque `oc`) :

```bash
docker build -t ghislainadon/ocp-toolkit:latest .
docker run --rm -it \
  -v ~/.kube:/root/.kube \
  -v "$PWD:/work" \
  ghislainadon/ocp-toolkit:latest validate
```

**Dans le cluster** (Job — pour les environnements sans accès direct) :

```bash
oc apply -f deploy/k8s/rbac.yaml -f deploy/k8s/job-validate.yaml
```

**Air-gapped** : pousser l'image dans le registre miroir
(`oc image mirror ghislainadon/ocp-toolkit:latest <mirror>/ocp-toolkit:latest`),
puis passer `--registry-mirror <mirror>` aux commandes — l'image des pods de
test et les références d'images migrées sont réécrites automatiquement.

## Sécurité et idempotence (règles transverses)

- **Dry-run par défaut** : toute action touchant un cluster exige `--confirm`.
- **Idempotent** : uniquement `kubectl/oc apply` (jamais `create`), ressources
  étiquetées `app.kubernetes.io/managed-by: ocp-toolkit`, relançable à chaque
  vague sans duplication.
- **Logs** : chaque exécution écrit `logs/ocptk-<timestamp>.log` (commandes
  exécutées incluses).

---

## Module 1 — `ocptk validate`

Valide que chaque nœud migré est pleinement fonctionnel sous OpenShift.

**Architecture d'échelle** : jamais de test séquentiel nœud par nœud.

1. **Checks plan de contrôle** (1 appel API pour 5000 nœuds) : nœud `Ready`,
   pressions ressources, runtime **CRI-O**, état **MachineConfig** (annotations
   MCO + synthèse des MachineConfigPools), labels/taints par rôle.
2. **Checks in-node** via un **DaemonSet de smoke-test** (un pod par nœud, SCC
   `restricted-v2` volontairement — c'est un des checks) : résolution DNS +
   réseau de services, **pod-à-pod inter-nœuds** (connexion aux pods CoreDNS
   d'autres nœuds), registre interne, egress (`--egress-target host:port`),
   **SELinux** (contexte `container_t`), **SCC effective** (downward API).
   Chaque pod imprime un résultat JSON ; l'orchestrateur collecte les logs en
   parallèle (`--parallel`, défaut 30).
3. **Stockage CSI échantillonné** (`--pvc-storageclass <sc> --pvc-sample 5`) :
   création + montage + écriture d'un PVC de test sur N nœuds, puis nettoyage.
   Échantillonné sciemment : 5000 PVC simultanés écrouleraient le stockage.
4. **Conformance cluster** : `--opct` guide/oriente vers OPCT (Sonobuoy +
   openshift-tests) pour la conformance globale, complémentaire du par-nœud.

**RBAC restreint** (testé sur Red Hat Developer Sandbox) : si l'opérateur ne
peut pas lister les nœuds (`cluster-reader` absent), les checks API sont
ignorés proprement et le rapport se construit depuis les résultats des pods.
Si le namespace cible existe déjà (`--namespace <projet>`), il est réutilisé
sans être ni modifié ni supprimé — seules les ressources ocptk sont nettoyées.

**Rapports** : `reports/validate-<timestamp>/` — `report.json` (machine),
`report.html` (chef d'équipe), `junit.xml` (CI) + **diff avec la vague
précédente** (régressions, nouveaux échecs, corrections, persistants).

```bash
ocptk validate                                  # dry-run : checks API + validation manifestes
ocptk validate --confirm --tolerate-all         # exécution réelle, tous les nœuds
ocptk validate --confirm --node-selector node-role.kubernetes.io/worker= \
  --pvc-storageclass ocs-storagecluster-ceph-rbd --egress-target proxy.corp:3128
```

## Module 2 — `ocptk provision`

Assistant interactif (ou `--answers reponses.yaml` pour la CI) qui génère dans
`generated/install-<cluster>/` :

- `install-config.yaml` (validé : CIDR, VIPs, pull-secret JSON, clé SSH…)
- `agent-config.yaml` (**Agent-based Installer** — méthode privilégiée
  bare-metal/air-gapped), squelette `hosts:` à compléter
- `README-install.md` (prérequis : binaires, DNS, LB, NTP…) et `.gitignore`
  (l'install-config contient le pull-secret : jamais dans Git, mode 0600)
- `answers.yaml` rejouable (sans les secrets)

Air-gapped : section `imageDigestSources` + `additionalTrustBundle` générées si
un registre miroir est déclaré. Avec `--confirm` et `openshift-install` présent,
l'outil enchaîne sur `agent create image` puis affiche les `wait-for`.

## Module 3 — `ocptk migrate`

Transforme des ressources K8s vanilla (dossier YAML **ou** cluster source) en
manifestes OpenShift propres, organisés **par projet et par application** en
structure **Kustomize** directement consommable par ArgoCD :

```
generated/migrate/<projet>/
├── kustomization.yaml            # namespace + applications
├── namespace.yaml                # annoté projet OpenShift
└── <application>/
    ├── README.md                 # transformations + points de revue humaine
    └── base/
        ├── kustomization.yaml
        └── *.yaml
```

Transformers (unitaires, testés — `go test ./...`) :

- `Ingress` → `Route` (host, path, TLS edge, backend ; warning sur les
  certificats à reporter)
- `Namespace` → annotations projet OpenShift
- **SCC / restricted-v2** (piège n°1) : `runAsUser`/`fsGroup` en dur détectés
  (corrigés avec `--fix-scc`) ; `privileged`, `hostNetwork`, `hostPath`,
  capabilities → **toujours WARN, jamais auto-corrigés**
- Images → réécriture registre interne/miroir (`--image-rewrite src=dst`,
  répétable ; `--registry-mirror` préfixe docker.io/quay.io/registry.k8s.io)
- `Service NodePort` → signalé, converti ClusterIP avec `--fix-nodeport`
- `PVC` → mapping `--storage-class-map gp2=ocs-…` ; ConfigMap/Secret conservés
  (warning re-chiffrement GitOps pour les Secrets)

**Positionnement** : OpenShift a déjà un contrôleur Ingress→Route à la volée,
et Red Hat fournit **MTC (Migration Toolkit for Containers) / Crane** pour la
migration d'état et de volumes. Cet outil ne réinvente pas la migration de
données : il produit des manifestes GitOps de première classe et signale ce qui
relève de MTC/Crane.

```bash
ocptk migrate --source-dir ./exports --fix-scc --fix-nodeport --confirm
ocptk migrate --from-cluster --namespaces shop,billing \
  --image-rewrite docker.io=registry.apps.ocp.corp/mirror --confirm
```

Rapport : `reports/migrate-<timestamp>/` (JSON + HTML + JUnit), synthèse par
application avec les WARN à traiter.

---

## Arborescence

```
ocp-toolkit/
├── cmd/ocptk/               # main
├── internal/
│   ├── cli/                 # menu principal + sous-commandes (cobra)
│   ├── common/
│   │   ├── kube/            # wrapper oc/kubectl (shell-out volontaire)
│   │   ├── logging/         # log fichier structuré
│   │   ├── report/          # JSON + HTML + JUnit + diff entre vagues
│   │   └── ui/              # prompts, tableaux, couleurs (zéro dépendance)
│   ├── validate/            # MODULE 1 (DaemonSet embarqué)
│   ├── provision/           # MODULE 2 (templates embarqués)
│   └── migrate/             # MODULE 3 (+ transform/ testé unitairement)
├── deploy/k8s/              # RBAC + Job pour exécution in-cluster
├── manifests/               # généré : DaemonSet de test rendu
├── generated/               # généré : configs install, arborescences Kustomize
├── reports/                 # généré : rapports horodatés
└── logs/
```

## Développement

```bash
make test      # go vet + tests unitaires (transformers, rapports/diff)
make build     # binaire local
# Sans Go installé :
docker run --rm -v "$PWD:/src" -w /src golang:1.24 sh -c "go vet ./... && go test ./..."
```
