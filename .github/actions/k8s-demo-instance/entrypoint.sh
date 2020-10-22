#!/bin/bash
set -e
source ".github/base-dockerfile/helpers/params.sh"
source ".github/base-dockerfile/helpers/tmp-helper.sh"

make_creds
echo "$INPUT_KUBE_CONFIG_DATA" >> kubeconfig
export KUBECONFIG="./kubeconfig"

helm install "${K_SERVICE}" samples/helm/sample-service/ \
    --set broker.auth.username="${K_DEFAULT_USER}" \
    --set broker.auth.password="${K_DEFAULT_PASS}" \
    --namespace "${K_NAMESPACE}" --wait --timeout 60m

kubectl get all -n "${K_NAMESPACE}"
