#!/bin/bash

set -e # exit on error

[ -n "$DEBUG" ] && set -x # echo commands if DEBUG is set

SERVERWAIT=1200 # seconds to wait for server to be ready
K3SWAIT=300    # additional seconds to wait for k3s to be ready
PROJECT_ID=${CHERRY_PROJECT_ID:?Need to set CHERRY_PROJECT_ID env var}
PLAN=${PLAN:-}
REGION=${REGION:-LT-Siauliai}
MIN_FREE=${MIN_FREE:-10} # minimum free servers in region
MIN_MEMORY=${MIN_MEMORY:-6} # GB
PLAN_TYPE=${PLAN_TYPE:-vps} # plan type to select
IMAGE=${IMAGE:-ubuntu_24_04_64bit}
PARTITION_SIZE=${PARTITION_SIZE:-40} # GB
KUBERNETES_DISTRO=${KUBERNETES_DISTRO:-k3s}
K8S_VERSION=${K8S_VERSION:?Need to set K8S_VERSION env var}
CCM_PATH=${CCM_PATH:?Need to set CCM_PATH env var}
CHERRY_AUTH_TOKEN=${CHERRY_AUTH_TOKEN:?Need to set CHERRY_AUTH_TOKEN env var}

# Create temporary directory for SSH keys
TEMP_DIR=$(mktemp -d)
chmod 0700 "$TEMP_DIR"
SSH_PRIVATE_KEY="$TEMP_DIR/id_rsa"
SSH_PUBLIC_KEY="$TEMP_DIR/id_rsa.pub"

# Cleanup function to remove temporary directory and SSH key
cleanup() {
    if [ -n "$NO_CLEANUP" ]; then
        echo "NO_CLEANUP is set, skipping cleanup"
        echo "Temporary directory retained at: $TEMP_DIR"
        echo "SSH key retained with ID: $SSH_KEY_ID"
        return
    fi
    echo "Cleaning up temporary directory: $TEMP_DIR"
    rm -rf "$TEMP_DIR"
    if [ ! -z "$SSH_KEY_ID" ]; then
        echo "Removing SSH key $SSH_KEY_ID from Cherry Servers..."
        cherryctl ssh-key delete -f -i $SSH_KEY_ID 2>/dev/null || echo "Failed to delete SSH key (it may have already been removed)"
    fi
    if [ -n "$ID_CONTROLPLANE" ]; then
        echo "Removing control plane server $ID_CONTROLPLANE..."
        cherryctl server delete -f $ID_CONTROLPLANE 2>/dev/null || echo "Failed to delete control plane server (it may have already been removed)"
    fi
    if [ -n "$ID_WORKER" ]; then
        echo "Removing worker server $ID_WORKER..."
        cherryctl server delete -f $ID_WORKER 2>/dev/null || echo "Failed to delete worker server (it may have already been removed)"
    fi
}
trap cleanup EXIT

normalize_k8s_version() {
    local k8s_version="$1"
    local normalized_version

    normalized_version="${k8s_version#v}"
    if ! [[ "$normalized_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo "K8S_VERSION must be an exact Kubernetes version like 1.33.10 or v1.33.10" >&2
        return 1
    fi

    echo "$normalized_version"
}

resolve_k3s_version() {
    local k8s_version="$1"
    local releases
    local resolved_version

    echo "Resolving latest k3s release for Kubernetes v${k8s_version}..." >&2
    releases=$(curl -sfL "https://api.github.com/repos/k3s-io/k3s/releases?per_page=100") || {
        echo "Failed to fetch k3s releases from GitHub" >&2
        return 1
    }

    resolved_version=$(echo "$releases" | jq -r --arg version "$k8s_version" '
        map(select(.prerelease == false and .draft == false))
        | map(.tag_name)
        | map(select(test("^v" + $version + "\\+k3s[0-9]+$")))
        | .[0] // empty
    ')

    if [ -z "$resolved_version" ]; then
        echo "No released k3s version found for Kubernetes v${k8s_version}" >&2
        return 1
    fi

    echo "$resolved_version"
}

wait_for_remote_marker() {
    local host="$1"
    local marker_file="$2"
    local wait_seconds="$3"
    local description="$4"
    local passed=0
    local interval=30

    echo "waiting for ${description} on server ${host}"
    while [ $passed -lt $wait_seconds ]; do
        if ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@"${host}" "test -f ${marker_file}" 2>/dev/null; then
            echo "${description} is ready on server ${host}"
            return 0
        fi
        echo "${description} not ready yet, waiting ${interval} seconds..."
        sleep $interval
        passed=$(($passed + $interval))
    done

    return 1
}

wait_for_node_ready() {
    local node_name="$1"
    local wait_seconds="$2"
    local description="$3"
    local passed=0
    local interval=15

    echo "waiting for ${description}"
    while [ $passed -lt $wait_seconds ]; do
        if kubectl get node "${node_name}" 2>/dev/null | grep -q -i -w ready; then
            echo "${description} is ready"
            return 0
        fi
        echo "${description} not ready yet, waiting ${interval} seconds..."
        sleep $interval
        passed=$(($passed + $interval))
    done

    return 1
}

deploy_cni_if_needed() {
    if [ "$KUBERNETES_DISTRO" != "kubeadm" ]; then
        return 0
    fi

    echo "deploying flannel CNI for kubeadm cluster"
    FLANNEL_VERSION=$(curl -sL https://api.github.com/repos/flannel-io/flannel/releases | jq -r 'map(select(.draft == false and .prerelease == false))[0].tag_name')
    if [ -z "$FLANNEL_VERSION" ] || [ "$FLANNEL_VERSION" = "null" ]; then
        echo "Failed to determine flannel release version"
        return 1
    fi
    kubectl apply -f "https://github.com/flannel-io/flannel/releases/download/${FLANNEL_VERSION}/kube-flannel.yml"
}

# find a slug for us
if [ -z "$PLAN" ]; then
    echo "No PLAN specified, selecting one automatically"
    echo "Selecting a plan of type ${PLAN_TYPE} with at least ${MIN_MEMORY}GB RAM in region $REGION with at least $MIN_FREE free servers"
    PLAN=$(cherryctl plans list -o json | jq -r 'first(.[] | select(.type == "'${PLAN_TYPE}'") | select(.specs.memory.total >= '${MIN_MEMORY}') | select(
      any(.available_regions[];
        .slug == "'${REGION}'" and .stock_qty > '${MIN_FREE}'
      )
    ) | .slug)')
    if [ -z "$PLAN" ]; then
        echo "Failed to find a suitable plan of type ${PLAN_TYPE} in region $REGION with at least $MIN_FREE free servers"
        exit 1
    fi
    echo "Selected plan: $PLAN"
fi
cherryctl plans list -o json | jq -r 'first(.[] | select(.type == "vps") | select(.specs.memory.total >= 6) | select(
      any(.available_regions[];
        .slug == "'${REGION}'" and .stock_qty > '${MIN_FREE}'
      )
    ) | .slug)'

echo "Generating SSH key pair..."
ssh-keygen -t rsa -b 4096 -f "$SSH_PRIVATE_KEY" -N "" -C "k8s-ccm-test-$(date +%s)"
chmod 0600 "$SSH_PRIVATE_KEY"

echo "Creating SSH key in Cherry Servers..."
SSH_KEY_LABEL="k8s-ccm-test-$(date +%s)"
SSH_KEY_RESULT=$(cherryctl ssh-key create --output json --key "$(cat "$SSH_PUBLIC_KEY")" --label "$SSH_KEY_LABEL")
SSH_KEY_ID=$(echo "$SSH_KEY_RESULT" | jq -r '.id')
echo "Created SSH key with ID: $SSH_KEY_ID"

NORMALIZED_K8S_VERSION=$(normalize_k8s_version "$K8S_VERSION")
K8S_MINOR_VERSION=$(echo "$NORMALIZED_K8S_VERSION" | cut -d. -f1,2)

case "$KUBERNETES_DISTRO" in
    k3s)
        K3S_VERSION=$(resolve_k3s_version "$NORMALIZED_K8S_VERSION")
        echo "Using k3s version: $K3S_VERSION"
        CONTROLPLANE_USERDATA_TEMPLATE="$(dirname "$0")/k3s-control-userdata.yaml"
        WORKER_USERDATA_TEMPLATE="$(dirname "$0")/k3s-worker-userdata-tmpl.yaml"
        CONTROLPLANE_READY_MARKER="/var/log/k3s-ready"
        CONTROLPLANE_KUBECONFIG_REMOTE="/etc/rancher/k3s/k3s.yaml"
        ;;
    kubeadm)
        echo "Using kubeadm Kubernetes version: v${NORMALIZED_K8S_VERSION}"
        CONTROLPLANE_USERDATA_TEMPLATE="$(dirname "$0")/kubeadm-control-userdata.yaml"
        WORKER_USERDATA_TEMPLATE="$(dirname "$0")/kubeadm-worker-userdata-tmpl.yaml"
        CONTROLPLANE_READY_MARKER="/var/log/kubeadm-ready"
        CONTROLPLANE_KUBECONFIG_REMOTE="/etc/kubernetes/admin.conf"
        ;;
    *)
        echo "Unsupported KUBERNETES_DISTRO: ${KUBERNETES_DISTRO}. Expected one of: k3s, kubeadm"
        exit 1
        ;;
esac

# determine if we set the partition size or not
PARTITION_ARG=""
if [ "$PLAN_TYPE" = "baremetal" ]; then
    PARTITION_ARG="--os-partition-size ${PARTITION_SIZE}"
fi

echo "deploying control plane server with ${KUBERNETES_DISTRO}"
USERDATA_FILE_CONTROLPLANE="${TEMP_DIR}/${KUBERNETES_DISTRO}-control-userdata.yaml"
cat "$CONTROLPLANE_USERDATA_TEMPLATE" \
    | sed "s|{{K3S_VERSION}}|${K3S_VERSION}|g" \
    | sed "s|{{K8S_VERSION}}|${NORMALIZED_K8S_VERSION}|g" \
    | sed "s|{{K8S_MINOR_VERSION}}|${K8S_MINOR_VERSION}|g" \
    > "$USERDATA_FILE_CONTROLPLANE"
RESULT=$(cherryctl server create --output json \
    --project-id ${PROJECT_ID} --hostname k8s-ccm-test-controlplane-1 \
    --plan ${PLAN} --region ${REGION} --image ${IMAGE} ${PARTITION_ARG} \
    --ssh-keys ${SSH_KEY_ID} \
    --userdata-file "${USERDATA_FILE_CONTROLPLANE}")

ID_CONTROLPLANE=$(echo $RESULT | jq -r '.id')
echo "waiting for server ${ID_CONTROLPLANE} to be ready"

# wait up to $SERVERWAIT seconds for the server to be active
PASSED=0
INTERVAL=30
STATE=""
while [ $PASSED -lt $SERVERWAIT ]; do
    STATE=$(cherryctl server get $ID_CONTROLPLANE --output json | jq -r '.state')
    if [ "$STATE" = "active" ]; then
        echo "control plane server $ID_CONTROLPLANE state 'active', success"
        break
    else
        echo "control plane server $ID_CONTROLPLANE state '$STATE', waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$STATE" != "active" ]; then
	echo "control plane server $ID_CONTROLPLANE did not become active in $SERVERWAIT seconds, exiting"
	exit 1
fi

# Get server IP for cluster readiness check
IP_CONTROLPLANE=$(cherryctl server get $ID_CONTROLPLANE --output json | jq -r '.ip_addresses[] | select(.type == "primary-ip") | .address')
[ -z "$IP_CONTROLPLANE" ] && { echo "Failed to get server IP address"; exit 1; }
echo "server $ID_CONTROLPLANE deployed successfully at IP $IP_CONTROLPLANE, now checking ${KUBERNETES_DISTRO} readiness..."

if ! wait_for_remote_marker "$IP_CONTROLPLANE" "$CONTROLPLANE_READY_MARKER" "$K3SWAIT" "control plane ${KUBERNETES_DISTRO}"; then
    echo "control plane server ${KUBERNETES_DISTRO} did not become ready in $K3SWAIT seconds"
    echo "You can check cluster status manually by connecting to the server:"
    echo "  ssh -i \"$SSH_PRIVATE_KEY\" root@$IP_CONTROLPLANE"
    if [ "$KUBERNETES_DISTRO" = "k3s" ]; then
        echo "  systemctl status k3s"
    else
        echo "  systemctl status kubelet"
        echo "  crictl ps -a"
    fi
    echo "  kubectl get nodes"
    exit 1
fi

# retrieve the kubeconfig and any worker bootstrap credentials
export KUBECONFIG=${TEMP_DIR}/kubeconfig
if [ "$KUBERNETES_DISTRO" = "k3s" ]; then
    K3S_TOKEN=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE} "cat /var/lib/rancher/k3s/server/node-token")
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE}:${CONTROLPLANE_KUBECONFIG_REMOTE} ${KUBECONFIG}.orig
    sed "s#127.0.0.1#$IP_CONTROLPLANE#g" ${KUBECONFIG}.orig > $KUBECONFIG
else
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE}:${CONTROLPLANE_KUBECONFIG_REMOTE} $KUBECONFIG
    KUBEADM_JOIN_COMMAND=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE} "kubeadm token create --ttl 2h --print-join-command")
    KUBEADM_TOKEN=$(echo "$KUBEADM_JOIN_COMMAND" | awk '{for (i = 1; i <= NF; i++) if ($i == "--token") {print $(i+1); exit}}')
    KUBEADM_DISCOVERY_TOKEN_CA_CERT_HASH=$(echo "$KUBEADM_JOIN_COMMAND" | awk '{for (i = 1; i <= NF; i++) if ($i == "--discovery-token-ca-cert-hash") {print $(i+1); exit}}')
    if [ -z "$KUBEADM_TOKEN" ] || [ -z "$KUBEADM_DISCOVERY_TOKEN_CA_CERT_HASH" ]; then
        echo "Failed to parse kubeadm join command: $KUBEADM_JOIN_COMMAND"
        exit 1
    fi
fi


# this will error out if it fails
kubectl get nodes

echo "${KUBERNETES_DISTRO} control plane is ready!"
echo "You can access the server via:"
echo "    ssh -i \"$SSH_PRIVATE_KEY\" root@${IP_CONTROLPLANE}"
echo "You can access kubernetes via:"
echo "    KUBECONFIG=${KUBECONFIG} kubectl get nodes"
echo ""
echo "Note: The SSH key will be cleaned up when the script exits."

# get the cluster UID
CLUSTER_UID=$(kubectl get namespace kube-system -o jsonpath='{.metadata.uid}')
echo "Cluster UID: $CLUSTER_UID"

## Deploy the CCM
# Assumes it already is built and available as a binary locally
echo "deploying CCM"

MANIFESTS_DIR="$(dirname "$0")/manifests"
cat "$MANIFESTS_DIR/ccm-test-secret.yaml" | sed "s/{{APIKEY}}/${CHERRY_AUTH_TOKEN}/g" | sed "s/{{PROJECTID}}/${PROJECT_ID}/g" | kubectl apply -f -

# deploy our manifest, replacing the actual image with a plain alpine:3.22 image,
# then copy the cloud-provider binary over and run it.
kubectl apply -f "$MANIFESTS_DIR/ccm-test-deployment.yaml"
# wait a few seconds for the pod to be created
PASSED=0
INTERVAL=1
CCM_WAIT=30
CCM_READY=false
while [ $PASSED -lt $CCM_WAIT ]; do
    PODSTATUS="$(kubectl -n kube-system get pod \
        -l app=cloud-provider-cherry \
        -o jsonpath='{.items[*].status.phase}' 2>/dev/null)"
    # Check if k3s-ready marker file exists
    if echo "$PODSTATUS" | grep -qw "Running"; then
        echo "CCM pod is ready"
        CCM_READY=true
        break
    else
        echo "CCM pod not ready yet, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$CCM_READY" != "true" ]; then
    echo "CCM pod did not become ready in $CCM_WAIT seconds"
    echo "You can check the pod status manually by running:"
    echo "  kubectl -n kube-system get pods -l app=cloud-provider-cherry"
    exit 1
fi

POD=$(kubectl -n kube-system get pod -l app=cloud-provider-cherry -o jsonpath='{.items[0].metadata.name}')

CCM_SCRIPT=${TEMP_DIR}/start-ccm.sh
cat > ${CCM_SCRIPT} << EOF
#!/bin/sh
# setsid so it keeps running after we exit the kubectl exec
CHERRY_REGION_NAME=${REGION} CHERRY_LOAD_BALANCER=metallb:// \
    setsid /cloud-provider-cherry \
    --cloud-provider=cherryservers --cloud-config=/etc/cloud-sa/cloud-sa.json \
    --leader-elect=false --authentication-skip-lookup=true </dev/null
EOF
kubectl -n kube-system cp ${CCM_PATH} ${POD}:/cloud-provider-cherry
kubectl -n kube-system cp ${CCM_SCRIPT} ${POD}:/start-ccm.sh
kubectl -n kube-system exec ${POD} -- sh -c "chmod +x /cloud-provider-cherry && chmod +x /start-ccm.sh"
kubectl -n kube-system exec ${POD} -- sh -c "/start-ccm.sh >& /var/log/cloud-provider-cherry.log &"

if ! deploy_cni_if_needed; then
    echo "Failed to deploy CNI for kubeadm cluster"
    exit 1
fi

if [ "$KUBERNETES_DISTRO" = "kubeadm" ]; then
    if ! wait_for_node_ready "k8s-ccm-test-controlplane-1" "$K3SWAIT" "control plane node k8s-ccm-test-controlplane-1"; then
        echo "control plane node did not become Ready in $K3SWAIT seconds after CNI deployment"
        exit 1
    fi
fi

# deploy a worker node
# first create userdata file with cluster join settings
WORKER_USERDATA_FILE="${TEMP_DIR}/${KUBERNETES_DISTRO}-worker-userdata.yaml"
cat "$WORKER_USERDATA_TEMPLATE" \
    | sed "s|{{CONTROL_PLANE_IP}}|${IP_CONTROLPLANE}|g" \
    | sed "s|{{CONTROL_PLANE_ENDPOINT}}|${IP_CONTROLPLANE}|g" \
    | sed "s|{{K3S_TOKEN}}|${K3S_TOKEN}|g" \
    | sed "s|{{K3S_VERSION}}|${K3S_VERSION}|g" \
    | sed "s|{{K8S_VERSION}}|${NORMALIZED_K8S_VERSION}|g" \
    | sed "s|{{K8S_MINOR_VERSION}}|${K8S_MINOR_VERSION}|g" \
    | sed "s|{{KUBEADM_TOKEN}}|${KUBEADM_TOKEN}|g" \
    | sed "s|{{DISCOVERY_TOKEN_CA_CERT_HASH}}|${KUBEADM_DISCOVERY_TOKEN_CA_CERT_HASH}|g" \
    > "${WORKER_USERDATA_FILE}"
echo "Created worker userdata file at: $WORKER_USERDATA_FILE"

# create a worker node
echo "deploying worker with ${KUBERNETES_DISTRO}"
PARTITION_ARG=""
if [ "$PLAN_TYPE" = "baremetal" ]; then
    PARTITION_ARG="--os-partition-size ${PARTITION_SIZE}"
fi
RESULT=$(cherryctl server create --output json \
    --project-id ${PROJECT_ID} --hostname k8s-ccm-test-worker-1 \
    --plan ${PLAN} --region ${REGION} --image ${IMAGE} ${PARTITION_ARG} \
    --ssh-keys ${SSH_KEY_ID} \
    --userdata-file "${WORKER_USERDATA_FILE}")

ID_WORKER=$(echo $RESULT | jq -r '.id')

echo "waiting for worker ${ID_WORKER} to be ready"

# wait up to $SERVERWAIT seconds for the server to be active
PASSED=0
INTERVAL=30
STATE=""
while [ $PASSED -lt $SERVERWAIT ]; do
    STATE=$(cherryctl server get $ID_WORKER --output json | jq -r '.state')
    if [ "$STATE" = "active" ]; then
        echo "worker server $ID_WORKER state 'active', success"
        break
    else
        echo "worker server $ID_WORKER state '$STATE', waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$STATE" != "active" ]; then
	echo "worker server $ID_WORKER did not become active in $SERVERWAIT seconds, exiting"
	exit 1
fi

# Get server IP for worker readiness check
IP_WORKER=$(cherryctl server get $ID_WORKER --output json | jq -r '.ip_addresses[] | select(.type == "primary-ip") | .address')
[ -z "$IP_WORKER" ] && { echo "Failed to get server IP address"; exit 1; }
echo "server $ID_WORKER deployed successfully at IP $IP_WORKER, now checking cluster readiness..."

if ! wait_for_node_ready "k8s-ccm-test-worker-1" "$K3SWAIT" "worker node k8s-ccm-test-worker-1"; then
    echo "worker node did not become ready in $K3SWAIT seconds"
    echo "You can check cluster status manually by connecting to the server:"
    echo "  ssh -i \"$SSH_PRIVATE_KEY\" root@$IP_CONTROLPLANE"
    echo "  kubectl get nodes"
    exit 1
fi

# wait 30 seconds to give the CCM time to update the node info
sleep 30

# check the node provider ID and that it matches
WORKER_NODE_DATA=$(kubectl get node "k8s-ccm-test-worker-1" -o json)
WORKER_NODE_PROVIDER_ID=$(echo "$WORKER_NODE_DATA" | jq -r '.spec.providerID')
WORKER_NODE_INSTANCE_TYPE=$(echo "$WORKER_NODE_DATA" | jq -r '.metadata.labels["node.kubernetes.io/instance-type"]')
WORKER_NODE_REGION=$(echo "$WORKER_NODE_DATA" | jq -r '.metadata.labels["topology.kubernetes.io/region"]')

if [ "$WORKER_NODE_PROVIDER_ID" = "cherryservers://$ID_WORKER" ]; then
    echo "worker node k8s-ccm-test-worker-1 provider ID matches as $WORKER_NODE_PROVIDER_ID"
else
    echo "worker node k8s-ccm-test-worker-1 provider ID does not match actual $WORKER_NODE_PROVIDER_ID vs expected cherryservers://$ID_WORKER"
    exit 1
fi
if [ "$WORKER_NODE_INSTANCE_TYPE" = "$PLAN" ]; then
    echo "worker node k8s-ccm-test-worker-1 instance type label matches as $WORKER_NODE_INSTANCE_TYPE"
else
    echo "worker node k8s-ccm-test-worker-1 instance type label does not match actual $WORKER_NODE_INSTANCE_TYPE vs expected $PLAN"
    exit 1
fi
if [ "$WORKER_NODE_REGION" = "$REGION" ]; then
    echo "worker node k8s-ccm-test-worker-1 region label matches as $WORKER_NODE_REGION"
else
    echo "worker node k8s-ccm-test-worker-1 region label does not match actual $WORKER_NODE_REGION vs expected $REGION"
    exit 1
fi

# remove the node in cherryservers, see that it disappears from the cluster
cherryctl server delete $ID_WORKER --force

echo "$(date) waiting for worker node to be removed from the cluster"
PASSED=0
INTERVAL=10
WORKER_REMOVED=false
WORKER_REMOVAL_WAIT=120
while [ $PASSED -lt $WORKER_REMOVAL_WAIT ]; do
    # Check if the worker node appears in kubectl get nodes
    if kubectl get node "k8s-ccm-test-worker-1" 2>&1 | grep -q 'NotFound'; then
        echo "$(date) worker node k8s-ccm-test-worker-1 has been removed from the cluster after server deletion"
        WORKER_REMOVED=true
        break
    else
        echo "worker node k8s-ccm-test-worker-1 is still present in the cluster after server deletion, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$WORKER_REMOVED" != "true" ]; then
    echo "$(date) worker node k8s-ccm-test-worker-1 was not removed from the cluster in $WORKER_REMOVAL_WAIT seconds after server deletion"
    exit 1
fi

## load balancer tests; we need to add a worker

echo "deploying worker with ${KUBERNETES_DISTRO}"
PARTITION_ARG=""
if [ "$PLAN_TYPE" = "baremetal" ]; then
    PARTITION_ARG="--os-partition-size ${PARTITION_SIZE}"
fi
RESULT=$(cherryctl server create --output json \
    --project-id ${PROJECT_ID} --hostname k8s-ccm-test-worker-1 \
    --plan ${PLAN} --region ${REGION} --image ${IMAGE} ${PARTITION_ARG} \
    --ssh-keys ${SSH_KEY_ID} \
    --userdata-file "${WORKER_USERDATA_FILE}")

ID_WORKER=$(echo $RESULT | jq -r '.id')

echo "waiting for worker ${ID_WORKER} to be ready"

# wait up to $SERVERWAIT seconds for the server to be active
PASSED=0
INTERVAL=30
STATE=""
while [ $PASSED -lt $SERVERWAIT ]; do
    STATE=$(cherryctl server get $ID_WORKER --output json | jq -r '.state')
    if [ "$STATE" = "active" ]; then
        echo "worker server $ID_WORKER state 'active', success"
        break
    else
        echo "worker server $ID_WORKER state '$STATE', waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$STATE" != "active" ]; then
	echo "worker server $ID_WORKER did not become active in $SERVERWAIT seconds, exiting"
	exit 1
fi

# Get server IP for worker readiness check
IP_WORKER=$(cherryctl server get $ID_WORKER --output json | jq -r '.ip_addresses[] | select(.type == "primary-ip") | .address')
[ -z "$IP_WORKER" ] && { echo "Failed to get server IP address"; exit 1; }
echo "server $ID_WORKER deployed successfully at IP $IP_WORKER, now checking cluster readiness..."

if ! wait_for_node_ready "k8s-ccm-test-worker-1" "$K3SWAIT" "worker node k8s-ccm-test-worker-1"; then
    echo "worker node did not become ready in $K3SWAIT seconds"
    echo "You can check cluster status manually by connecting to the server:"
    echo "  ssh -i \"$SSH_PRIVATE_KEY\" root@$IP_CONTROLPLANE"
    echo "  kubectl get nodes"
    exit 1
fi

## deploy metallb
METALLB_VERSION=$(curl -sL https://api.github.com/repos/metallb/metallb/releases | jq -r ".[0].tag_name")
kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml

# wait 30 seconds to be safe
sleep 30

# deploy nginx service 1
kubectl apply -f "$MANIFESTS_DIR/nginx-1.yaml"
# wait for the service to get an IP
echo "waiting for nginx-1 service to get an external IP"
PASSED=0
INTERVAL=10
FIP_WAIT=30
SVC_READY=false
NGINX1_IP=""
while [ $PASSED -lt $FIP_WAIT ]; do
    NGINX1_IP=$(kubectl get svc nginx-1 -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
    if [ -n "$NGINX1_IP" ]; then
        echo "nginx-1 service has external IP: $NGINX1_IP"
        SVC_READY=true
        break
    else
        echo "nginx-1 service does not have an external IP yet, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$SVC_READY" != "true" ]; then
    echo "nginx-1 service did not get an external IP in $FIP_WAIT seconds"
    exit 1
fi
# get the floating IP for it
NGINX1_FIP_JSON=$(cherryctl ip list -o json | jq -r '.[] | select(.address == "'$NGINX1_IP'")')
if [ -z "$NGINX1_FIP_JSON" ]; then
    echo "nginx-1 service does not have a floating IP"
    exit 1
fi
echo "nginx-1 service floating IP: $NGINX1_IP"

# check the tags
NGINX1_FIP_TAG_USAGE=$(echo "$NGINX1_FIP_JSON" | jq -r '.tags.usage')
if [ "$NGINX1_FIP_TAG_USAGE" != "cloud-provider-cherry-auto" ]; then
    echo "nginx-1 service floating IP does not have correct usage tag, got: $NGINX1_FIP_TAG_USAGE"
    exit 1
fi
NGINX1_FIP_TAG_CLUSTER=$(echo "$NGINX1_FIP_JSON" | jq -r '.tags.cluster')
if [ "$NGINX1_FIP_TAG_CLUSTER" != "$CLUSTER_UID" ]; then
    echo "nginx-1 service floating IP does not have correct cluster tag, got $NGINX1_FIP_TAG_CLUSTER, expected $CLUSTER_UID"
    exit 1
fi
NGINX1_FIP_TAG_SERICE=$(echo "$NGINX1_FIP_JSON" | jq -r '.tags.service')
NGINX1_EXPECTED_SERVICE_TAG=$(printf 'default/nginx-1' | sha256sum | awk '{print $1}' | xxd -r -p | base64)
if [ "$NGINX1_FIP_TAG_SERICE" != "$NGINX1_EXPECTED_SERVICE_TAG" ]; then
    echo "nginx-1 service floating IP does not have correct service tag, got $NGINX1_FIP_TAG_SERICE, expected $NGINX1_EXPECTED_SERVICE_TAG"
    exit 1
fi

echo "nginx-1 service floating IP tags are correct"

# deploy nginx service 2
kubectl apply -f "$MANIFESTS_DIR/nginx-2.yaml"
# wait for the service to get an IP
echo "waiting for nginx-2 service to get an external IP"
PASSED=0
INTERVAL=10
FIP_WAIT=30
SVC_READY=false
NGINX2_IP=""
while [ $PASSED -lt $FIP_WAIT ]; do
    NGINX2_IP=$(kubectl get svc nginx-2 -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
    if [ -n "$NGINX2_IP" ]; then
        echo "nginx-2 service has external IP: $NGINX2_IP"
        SVC_READY=true
        break
    else
        echo "nginx-2 service does not have an external IP yet, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done
if [ "$SVC_READY" != "true" ]; then
    echo "nginx-2 service did not get an external IP in $FIP_WAIT seconds"
    exit 1
fi



# get the floating IP for it
NGINX2_FIP_JSON=$(cherryctl ip list -o json | jq -r '.[] | select(.address == "'$NGINX2_IP'")')
if [ -z "$NGINX2_FIP_JSON" ]; then
    echo "nginx-2 service does not have a floating IP"
    exit 1
fi
echo "nginx-2 service floating IP: $NGINX2_IP"

# check the tags
NGINX2_FIP_TAG_USAGE=$(echo "$NGINX2_FIP_JSON" | jq -r '.tags.usage')
if [ "$NGINX2_FIP_TAG_USAGE" != "cloud-provider-cherry-auto" ]; then
    echo "nginx-2 service floating IP does not have correct usage tag, got: $NGINX2_FIP_TAG_USAGE"
    exit 1
fi
NGINX2_FIP_TAG_CLUSTER=$(echo "$NGINX2_FIP_JSON" | jq -r '.tags.cluster')
if [ "$NGINX2_FIP_TAG_CLUSTER" != "$CLUSTER_UID" ]; then
    echo "nginx-2 service floating IP does not have correct cluster tag, got $NGINX2_FIP_TAG_CLUSTER, expected $CLUSTER_UID"
    exit 1
fi
NGINX2_FIP_TAG_SERVICE=$(echo "$NGINX2_FIP_JSON" | jq -r '.tags.service')
NGINX2_EXPECTED_SERVICE_TAG=$(printf 'default/nginx-2' | sha256sum | awk '{print $1}' | xxd -r -p | base64)
if [ "$NGINX2_FIP_TAG_SERVICE" != "$NGINX2_EXPECTED_SERVICE_TAG" ]; then
    echo "nginx-2 service floating IP does not have correct service tag, got $NGINX2_FIP_TAG_SERVICE, expected $NGINX2_EXPECTED_SERVICE_TAG"
    exit 1
fi

echo "nginx-2 service floating IP tags are correct"


# see that svc1 still has a valid IP, and that it is distinct from svc2
NGINX1_IP=$(kubectl get svc nginx-1 -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
if [ -z "$NGINX1_IP" ]; then
    echo "nginx-1 service lost its external IP"
    exit 1
fi
if [ "$NGINX1_IP" = "$NGINX2_IP" ]; then
    echo "nginx-1 and nginx-2 services have the same external IP, expected distinct IPs"
    exit 1
fi

# delete service 2, check that the FIP goes away, and that service 1 is unaffected
kubectl delete -f "$MANIFESTS_DIR/nginx-2.yaml"
# wait for the IP to go away
echo "waiting for nginx-2 service external IP to be removed"
PASSED=0
INTERVAL=10
FIP_WAIT=30
SVC_GONE=false
while [ $PASSED -lt $FIP_WAIT ]; do
    NGINX2_FIP_JSON=$(cherryctl ip list -o json | jq -r '.[] | select(.address == "'$NGINX2_IP'")')
    if [ -z "$NGINX2_FIP_JSON" ]; then
        echo "nginx-2 service floating IP has been removed"
        SVC_GONE=true
        break
    else
        echo "nginx-2 service floating IP is still present, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$SVC_GONE" != "true" ]; then
    echo "nginx-2 service floating IP was not removed in $FIP_WAIT seconds after service deletion"
    exit 1
fi

# check that svc1 is unaffected
NGINX1_IP=$(kubectl get svc nginx-1 -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
if [ -z "$NGINX1_IP" ]; then
    echo "nginx-1 service lost its external IP after nginx-2 deletion"
    exit 1
fi

# delete service 1
kubectl delete -f "$MANIFESTS_DIR/nginx-1.yaml"
# wait for the IP to go away
echo "waiting for nginx-1 service external IP to be removed"
PASSED=0
INTERVAL=10
FIP_WAIT=30
SVC_GONE=false
while [ $PASSED -lt $FIP_WAIT ]; do
    NGINX1_FIP_JSON=$(cherryctl ip list -o json | jq -r '.[] | select(.address == "'$NGINX1_IP'")')
    if [ -z "$NGINX1_FIP_JSON" ]; then
        echo "nginx-1 service floating IP has been removed"
        SVC_GONE=true
        break
    else
        echo "nginx-1 service floating IP is still present, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done
if [ "$SVC_GONE" != "true" ]; then
    echo "nginx-1 service floating IP was not removed in $FIP_WAIT seconds after service deletion"
    exit 1
fi
echo "load balancer tests completed successfully"

    
