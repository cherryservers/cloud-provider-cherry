package e2etest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"time"

	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const informerResyncPeriod = 5 * time.Second

// ccmSecret generates the secret required for CCM deployment
// and returns a path to a temp file with it.
func ccmSecret(cfg ccm.Config) (path string, cleanup func(), err error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshall secret to json: %w", err)
	}

	f, err := os.CreateTemp("", "ccm-secret-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file for secret: %w", err)
	}
	path = f.Name()
	cleanup = fileCleanup(path)

	_, err = f.Write(data)
	if err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("failed to write secret to file: %w", err)
	}

	err = f.Close()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to close secret file: %w", err)
	}

	return path, cleanup, nil
}

// runCcm runs the CCM using the go toolchain as a child process.
// The child process is cancelled when the context is cancelled,
// but has a teardown process, which is done when `stopped` is closed.
func runCcm(ctx context.Context, kubeconfig, secret string, k8sClient kubernetes.Interface) (stopped <-chan struct{}, err error) {
	cmd := exec.CommandContext(ctx, "go", "run", "..", "--cloud-provider=cherryservers",
		"--leader-elect=false", "--authentication-skip-lookup=true",
		"--kubeconfig="+kubeconfig, "--cloud-config="+secret, "--v=2")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Println("failed to create stderr pipe for ccm process")
	} else {
		go io.Copy(log.Writer(), stderr)
	}

	// Need this for metallb client, which doesn't build
	// from the controller client builder and doesn't get the
	// flags from the command line.
	env := append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfig))
	cmd.Env = env

	err = cmd.Start()
	if err != nil {
		return nil, err
	}
	log.Printf("ccm pid: %d\n", cmd.Process.Pid)

	// Ensure graceful exit on teardown.
	stoppedCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		cmd.Wait()
		close(stoppedCh)
	}()

	informerCtx, cancel := context.WithCancel(ctx)

	factory := informers.NewSharedInformerFactory(k8sClient, informerResyncPeriod)
	_, err = factory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj any) {
			newNode, _ := newObj.(*corev1.Node)
			// if there's no taints, the node was successfully registered by the ccm
			if len(newNode.Spec.Taints) == 0 {
				log.Printf("reached 0 taints for node %s\n", newNode.ObjectMeta.Name)
				cancel()
			}
		}})
	if err != nil {
		return stoppedCh, fmt.Errorf("failed to add node event handler: %w", err)
	}

	factory.Start(informerCtx.Done())
	factory.WaitForCacheSync(informerCtx.Done())
	<-informerCtx.Done()
	factory.Shutdown()

	return stoppedCh, nil
}
