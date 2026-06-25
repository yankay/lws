#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

readonly DEFAULT_RELEASE_NAME="lws"
readonly DEFAULT_NAMESPACE="lws-system"
readonly DEFAULT_CHART="oci://registry.k8s.io/lws/charts/lws"
readonly LEADERWORKERSET_CRD="leaderworkersets.leaderworkerset.x-k8s.io"
readonly WEBHOOK_SERVICE="lws-webhook-service"

release_name="${DEFAULT_RELEASE_NAME}"
namespace="${DEFAULT_NAMESPACE}"
chart="${DEFAULT_CHART}"
chart_version=""
backup_dir=""

function usage {
  cat <<EOF
Prepare an existing LWS Helm release for a historical CRD layout upgrade.

This script does not run helm upgrade. It backs up the current release state,
protects the historical LeaderWorkerSet CRD from Helm deletion, applies the
target chart CRDs, patches the LeaderWorkerSet conversion webhook namespace, and
prints the helm upgrade command to run next.

Usage:
  hack/prepare-helm-legacy-crd-upgrade.sh --chart-version <version> [flags]

Flags:
  --chart-version <version>   Target LWS Helm chart version, for example 0.8.0. Required.
  --release-name <name>       Helm release name. Default: ${DEFAULT_RELEASE_NAME}
  --namespace <namespace>     Helm release namespace. Default: ${DEFAULT_NAMESPACE}
  --chart <chart>             Helm chart reference. Default: ${DEFAULT_CHART}
  --backup-dir <path>         Directory for backup files. Default: lws-helm-upgrade-backup-<timestamp>
  -h, --help                  Show this help message.
EOF
}

function log {
  echo "==> $*"
}

function warn {
  echo "WARNING: $*" >&2
}

function die {
  echo "ERROR: $*" >&2
  exit 1
}

function require_command {
  local command_name="$1"
  command -v "${command_name}" >/dev/null 2>&1 || die "${command_name} is required"
}

function parse_args {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --chart-version)
        [[ $# -ge 2 ]] || die "--chart-version requires a value"
        chart_version="$2"
        shift 2
        ;;
      --release-name)
        [[ $# -ge 2 ]] || die "--release-name requires a value"
        release_name="$2"
        shift 2
        ;;
      --namespace)
        [[ $# -ge 2 ]] || die "--namespace requires a value"
        namespace="$2"
        shift 2
        ;;
      --chart)
        [[ $# -ge 2 ]] || die "--chart requires a value"
        chart="$2"
        shift 2
        ;;
      --backup-dir)
        [[ $# -ge 2 ]] || die "--backup-dir requires a value"
        backup_dir="$2"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "unknown argument: $1"
        ;;
    esac
  done

  [[ -n "${chart_version}" ]] || die "--chart-version is required"
  if [[ -z "${backup_dir}" ]]; then
    backup_dir="lws-helm-upgrade-backup-$(date +%Y%m%d%H%M%S)"
  fi
}

function backup_release_state {
  log "Writing backup files to ${backup_dir}"
  mkdir -p "${backup_dir}"

  helm get values "${release_name}" --namespace "${namespace}" --all \
    > "${backup_dir}/helm-values.yaml"
  helm get manifest "${release_name}" --namespace "${namespace}" \
    > "${backup_dir}/helm-manifest.yaml"

  if kubectl get crd "${LEADERWORKERSET_CRD}" >/dev/null 2>&1; then
    kubectl get crd "${LEADERWORKERSET_CRD}" -o yaml \
      > "${backup_dir}/leaderworkerset-crd.yaml"
    kubectl get "${LEADERWORKERSET_CRD}" --all-namespaces -o yaml \
      > "${backup_dir}/leaderworkersets.yaml"
  else
    warn "CRD ${LEADERWORKERSET_CRD} was not found; LeaderWorkerSet objects cannot be backed up"
  fi
}

function annotate_crd_keep {
  local crd_name="$1"
  if kubectl get crd "${crd_name}" >/dev/null 2>&1; then
    kubectl annotate crd "${crd_name}" "helm.sh/resource-policy=keep" --overwrite
  else
    warn "CRD ${crd_name} was not found; skipping keep annotation"
  fi
}

function apply_target_crds {
  local crds_file
  local normalized_crds_file
  crds_file="$(mktemp)"
  normalized_crds_file="$(mktemp)"

  log "Fetching CRDs from ${chart} version ${chart_version}"
  if ! helm show crds "${chart}" --version "${chart_version}" > "${crds_file}"; then
    rm -f "${crds_file}"
    rm -f "${normalized_crds_file}"
    die "failed to fetch CRDs from ${chart} version ${chart_version}"
  fi
  if [[ ! -s "${crds_file}" ]]; then
    rm -f "${crds_file}"
    rm -f "${normalized_crds_file}"
    die "no CRDs were found in ${chart} version ${chart_version}"
  fi

  awk '
    $0 == "apiVersion: apiextensions.k8s.io/v1" {
      if (seen++) {
        print "---"
      }
    }
    { print }
  ' "${crds_file}" > "${normalized_crds_file}"

  log "Applying target chart CRDs"
  kubectl apply --server-side --force-conflicts -f "${normalized_crds_file}"
  rm -f "${crds_file}"
  rm -f "${normalized_crds_file}"
}

function patch_conversion_webhook {
  if ! kubectl get crd "${LEADERWORKERSET_CRD}" >/dev/null 2>&1; then
    warn "CRD ${LEADERWORKERSET_CRD} was not found; skipping conversion webhook patch"
    return
  fi

  log "Patching LeaderWorkerSet conversion webhook namespace"
  local conversion_patch
  conversion_patch=$(cat <<EOF
{"spec":{"conversion":{"strategy":"Webhook","webhook":{"conversionReviewVersions":["v1"],"clientConfig":{"service":{"namespace":"${namespace}","name":"${WEBHOOK_SERVICE}","path":"/convert"}}}}}}
EOF
)
  kubectl patch crd "${LEADERWORKERSET_CRD}" --type=merge -p "${conversion_patch}"
}

function print_next_steps {
  cat <<EOF

The LWS Helm release is prepared for upgrade.

Run helm upgrade with the same --values and --set options used by the existing
release, for example:

helm upgrade ${release_name} ${chart} \\
  --version ${chart_version} \\
  --namespace ${namespace} \\
  --wait --timeout 300s

EOF

  echo "Backup files were written to: ${backup_dir}"
}

parse_args "$@"

require_command helm
require_command kubectl

log "Checking Helm release ${release_name} in namespace ${namespace}"
helm status "${release_name}" --namespace "${namespace}" >/dev/null

backup_release_state

log "Protecting historical LeaderWorkerSet CRD before applying target CRDs"
annotate_crd_keep "${LEADERWORKERSET_CRD}"

apply_target_crds
patch_conversion_webhook

log "Re-applying keep annotation to the historical LeaderWorkerSet CRD"
annotate_crd_keep "${LEADERWORKERSET_CRD}"

print_next_steps
