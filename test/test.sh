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

# determine if we set the partition size or not
PARTITION_ARG=""
if [ "$PLAN_TYPE" = "baremetal" ]; then
    PARTITION_ARG="--os-partition-size ${PARTITION_SIZE}"
fi

echo "deploying control plane server with k3s"
USERDATA_FILE_CONTROLPLANE="$(dirname "$0")/k3s-control-userdata.yaml"
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

# Get server IP for k3s readiness check
IP_CONTROLPLANE=$(cherryctl server get $ID_CONTROLPLANE --output json | jq -r '.ip_addresses[] | select(.type == "primary-ip") | .address')
[ -z "$IP_CONTROLPLANE" ] && { echo "Failed to get server IP address"; exit 1; }
echo "server $ID_CONTROLPLANE deployed successfully at IP $IP_CONTROLPLANE, now checking k3s readiness..."

# Wait for k3s to be ready
echo "waiting for k3s to be ready on server $IP_CONTROLPLANE"
PASSED=0
INTERVAL=30
K3S_READY=false

while [ $PASSED -lt $K3SWAIT ]; do
    # Check if k3s-ready marker file exists
    if ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE} "test -f /var/log/k3s-ready" 2>/dev/null; then
        echo "control plane server k3s is ready on server $IP_CONTROLPLANE"
        K3S_READY=true
        break
    else
        echo "control plane server k3s not ready yet, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$K3S_READY" != "true" ]; then
    echo "control plane server k3s did not become ready in $K3SWAIT seconds"
    echo "You can check k3s status manually by connecting to the server:"
    echo "  ssh -i \"$SSH_PRIVATE_KEY\" root@$IP_CONTROLPLANE"
    echo "  systemctl status k3s"
    echo "  kubectl get nodes"
    exit 1
fi

# retrieve the kubeconfig and the token
export KUBECONFIG=${TEMP_DIR}/kubeconfig
K3S_TOKEN=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE} "cat /var/lib/rancher/k3s/server/node-token")
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$SSH_PRIVATE_KEY" root@${IP_CONTROLPLANE}:/etc/rancher/k3s/k3s.yaml ${KUBECONFIG}.orig
cat ${KUBECONFIG}.orig | sed "s#127.0.0.1#$IP_CONTROLPLANE#g" > $KUBECONFIG


# this will error out if it fails
kubectl get nodes

echo "k3s control plane is ready!"
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
    PODSTATUS=$(kubectl -n kube-system get pod -l app=cloud-provider-cherry -o jsonpath='{.items[0].status.phase}')
    # Check if k3s-ready marker file exists
    if [ "$PODSTATUS" = "Running" ]; then
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

# deploy a worker node
# first create userdata file with control plane IP
WORKER_USERDATA_TEMPLATE="$(dirname "$0")/k3s-worker-userdata-tmpl.yaml"
WORKER_USERDATA_FILE="${TEMP_DIR}/k3s-worker-userdata.yaml"
cat "$WORKER_USERDATA_TEMPLATE" | sed "s/{{CONTROL_PLANE_IP}}/${IP_CONTROLPLANE}/g" | sed "s/{{K3S_TOKEN}}/${K3S_TOKEN}/g" > "${WORKER_USERDATA_FILE}"
echo "Created worker userdata file at: $WORKER_USERDATA_FILE"

# create a worker node
echo "deploying worker with k3s"
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

# Get server IP for k3s readiness check
IP_WORKER=$(cherryctl server get $ID_WORKER --output json | jq -r '.ip_addresses[] | select(.type == "primary-ip") | .address')
[ -z "$IP_WORKER" ] && { echo "Failed to get server IP address"; exit 1; }
echo "server $ID_WORKER deployed successfully at IP $IP_WORKER, now checking k3s readiness..."

# wait for the worker to be ready in k3s
echo "waiting for worker node to be ready in k3s"
PASSED=0
INTERVAL=15
WORKER_READY=false
while [ $PASSED -lt $K3SWAIT ]; do
    # Check if the worker node appears in kubectl get nodes
    if kubectl get node "k8s-ccm-test-worker-1" | grep -q -i -w ready; then
        echo "worker node k8s-ccm-test-worker-1 is now ready in k3s"
        WORKER_READY=true
        break
    else
        echo "worker node not visible yet, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$WORKER_READY" != "true" ]; then
    echo "worker node did not become ready in $K3SWAIT seconds"
    echo "You can check k3s status manually by connecting to the server:"
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

echo "$(date) waiting for worker node to be removed from k3s"
PASSED=0
INTERVAL=10
WORKER_REMOVED=false
WORKER_REMOVAL_WAIT=120
while [ $PASSED -lt $WORKER_REMOVAL_WAIT ]; do
    # Check if the worker node appears in kubectl get nodes
    if kubectl get node "k8s-ccm-test-worker-1" 2>&1 | grep -q 'NotFound'; then
        echo "$(date) worker node k8s-ccm-test-worker-1 has been removed from k3s after server deletion"
        WORKER_REMOVED=true
        break
    else
        echo "worker node k8s-ccm-test-worker-1 is still present in k3s after server deletion, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$WORKER_REMOVED" != "true" ]; then
    echo "$(date) worker node k8s-ccm-test-worker-1 was not removed from k3s in $WORKER_REMOVAL_WAIT seconds after server deletion"
    exit 1
fi

## load balancer tests; we need to add a worker

echo "deploying worker with k3s"
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

# Get server IP for k3s readiness check
IP_WORKER=$(cherryctl server get $ID_WORKER --output json | jq -r '.ip_addresses[] | select(.type == "primary-ip") | .address')
[ -z "$IP_WORKER" ] && { echo "Failed to get server IP address"; exit 1; }
echo "server $ID_WORKER deployed successfully at IP $IP_WORKER, now checking k3s readiness..."

# wait for the worker to be ready in k3s
echo "waiting for worker node to be ready in k3s"
PASSED=0
INTERVAL=15
WORKER_READY=false
while [ $PASSED -lt $K3SWAIT ]; do
    # Check if the worker node appears in kubectl get nodes
    if kubectl get node "k8s-ccm-test-worker-1" | grep -q -i -w ready; then
        echo "worker node k8s-ccm-test-worker-1 is now ready in k3s"
        WORKER_READY=true
        break
    else
        echo "worker node not visible yet, waiting $INTERVAL seconds..."
        sleep $INTERVAL
        PASSED=$(($PASSED + $INTERVAL))
    fi
done

if [ "$WORKER_READY" != "true" ]; then
    echo "worker node did not become ready in $K3SWAIT seconds"
    echo "You can check k3s status manually by connecting to the server:"
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
NGINX1_EXPECTED_SERVICE_TAG=$(print 'default/nginx-1' | sha256sum | awk '{print $1}' | xxd -r -p | base64)
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
NGINX2_EXPECTED_SERVICE_TAG=$(print 'default/nginx-2' | sha256sum | awk '{print $1}' | xxd -r -p | base64)
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

    