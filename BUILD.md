# CCM for Cherry Servers Build and Design

The Cloud Controller Manager (CCM) Cherry Servers plugin enables a Kubernetes cluster to interface directly with
Cherry Servers cloud services.

## Deploy

Read how to deploy the Kubernetes CCM for Cherry Servers in the [README.md](./README.md)!

## Building

To build the binary, run:

```
make build
```

It will deposit the binary for your local architecture as `dist/bin/cloud-provider-cherry-$(OS)-$(ARCH)`

By default `make build` builds the binary using your locally installed go toolchain.
To build it using a docker container, do:

```
make build DOCKERBUILD=true
```

By default, it will build for your local operating system and CPU architecture. You can build for alternate architectures or operating systems via the `OS` and `ARCH` parameters:

```
make build OS=darwin
make build OS=linux ARCH=arm64
```

## Docker Image

To build a docker image, run:

```
make image
```

The image will be tagged with `:latest`

## Running Locally

You can run the CCM locally on your laptop or VM, i.e. not in the cluster. This _dramatically_ speeds up development. To do so:

1. Deploy everything except for the `Deployment` and, optionally, the `Secret`
1. Build it for your local platform `make build`
1. Set the environment variable `CCM_SECRET` to a file with the secret contents as a json, i.e. the content of the secret's `stringData`, e.g. `CCM_SECRET=ccm-secret.yaml`
1. Set the environment variable `KUBECONFIG` to a kubeconfig file with sufficient access to the cluster, e.g. `KUBECONFIG=mykubeconfig`
1. Set the environment variable `CHERRY_REGION_NAME` to the correct region where the cluster is running, e.g. `CHERRY_REGION_NAME="EU-Nord-1`
1. If you want to run a loadbalancer, and it is not yet deployed, deploy it appropriately.
1. Enable the loadbalancer by setting the environment variable `CHERRY_LOAD_BALANCER=metallb://`
1. If you want to use a managed Floating IP for the control plane, create one using the Cherry Servers API or Web UI, tag it uniquely, and set the environment variable `CHERRY_FIP_TAG=<tag>`
1. Run the command.

There are multiple ways to run the command.

In all cases, for lots of extra debugging, add `--v=2` or even higher levels, e.g. `--v=5`.

### Docker

```
docker run --rm -e CHERRY_REGION_NAME=${CHERRY_REGION_NAME} -e CHERRY_LOAD_BALANCER=${CHERRY_LOAD_BALANCER} cherryservers/cloud-provider-cherry:latest --cloud-provider=cherryservers --leader-elect=false --authentication-skip-lookup=true --cloud-config=$CCM_SECRET --kubeconfig=$KUBECONFIG
```

### Go toolchain

```
CHERRY_REGION_NAME=${CHERRY_REGION_NAME} CHERRY_LOAD_BALANCER=${CHERRY_LOAD_BALANCER} go run . --cloud-provider=cherryservers --leader-elect=false --authentication-skip-lookup=true --cloud-config=$CCM_SECRET --kubeconfig=$KUBECONFIG
```

### Locally compiled binary

```
CHERRY_REGION_NAME=${CHERRY_REGION_NAME} CHERRY_LOAD_BALANCER=metallb:// dist/bin/cloud-provider-cherry-darwin-amd64 --cloud-provider=cherryservers --leader-elect=false --authentication-skip-lookup=true --cloud-config=$CCM_SECRET --kubeconfig=$KUBECONFIG
```

## CI/CD/Release pipeline

The CI/CD/Release pipeline is run via the following steps:

* `make ci`: builds the binary, runs all tests, builds the OCI image
* `make cd`: takes the image from the prior stage, tags it with the name of the branch and git hash from the commit, and pushes to the docker registry
* `make release`: takes the image from the `ci` stage, tags it with the git tag, and pushes to the docker registry

The assumptions about workflow are as follows:

* `make ci` can be run anywhere. The built binaries and OCI image will be named and tagged as per `make build` and `make image` above.
* `make cd` should be run only on a merge into `main`. It generally will be run only in a CI system, e.g. [drone](https://drone.io) or [github actions](https://github.com/features/actions). It requires passing both `CONFIRM=true` to tell it that it is ok to push, and `BRANCH_NAME=${BRANCH_NAME}` to tell it what tag should be used in addition to the git hash. For example, to push out the current commit as main: `make cd CONFIRM=true BRANCH_NAME=main`
* `make release` should be run only on applying a tag to `main`, although it can run elsewhere. It generally will be run only in a CI system. It requires passing both `CONFIRM=true` to tell it that it is ok to push, and `RELEASE_TAG=${RELEASE_TAG}` to tell it what tag this release should be. For example, to push out a tagged version `v1.2.3` on the current commit: `make release CONFIRM=true RELEASE_TAG=v1.2.3`.

For both `make cd` and `make release`, if you wish to push out a _different_ commit, then check that one out first.

The flow to make changes normally should be:

1. `main` is untouched, a protected branch.
2. In your local copy, create a new working branch.
3. Make your changes in your working branch, commit and push.
4. Open a Pull Request or Merge Request from the branch to `main`. This will cause `make ci` to run.
5. When CI passes and maintainers approve, merge the PR/MR into `main`. This will cause `make ci` and `make cd CONFIRM=true BRANCH_NAME=main` to run, pushing out images tagged with `:main` and `:${GIT_HASH}`
6. When a particular commit is ready to cut a release, **on main** add a git tag and push. This will cause `make release CONFIRM=true RELEASE_TAG=<applied git tag>` to run, pushing out an image tagged with `:${RELEASE_TAG}`

## Design

The Cherry Servers CCM follows the standard design principles for external cloud controller managers.

The main entrypoint command is in [main.go](./main.go), and provides fairly standard boilerplate for CCM.

1. import the Cherry Servers implementation as `import _ "github.com/cherryservers/cloud-provider-cherry/cherry"`:
   1. calls `init()`, which..
   1. registers the Cherry Servers provider
1. import the main app from [k8s.io/kubernetes/cmd/cloud-controller-manager/app](https://godoc.org/k8s.io/kubernetes/cmd/cloud-controller-manager/app)
1. `main()`:
   1. initialize the command
	 1. call `command.Execute()`

The Cherry Servers-specific logic is in [github.com/cherryservers/cloud-provider-cherry/cherry](./cherry/), which, as described before,
is imported into `main.go`. The blank `import _` is used solely for the side-effects, i.e. to cause the `init()`
function in [cherry/cloud.go](./cherry/cloud.go) to run before executing the command. This `init()`
registers the Cherry Servers cloud provider via `cloudprovider.RegisterCloudProvider`, where `cloudprovider` is
aliased to [k8s.io/cloud-provider](https://godoc.org/k8s.io/cloud-provider).

The `init()` step does the following registers the Cherry Servers provider with the name `"cherryservers"` and an initializer
`func`, which:

1. retrieves the Cherry Servers project ID and Cherry Servers secret API token
1. creates a new [cherrygo.Client](https://godoc.org/github.com/cherryservers/cherrygo#Client)
1. creates a new [cherry.cloud](./cherry/cloud.go), passing it the client, so it can interact with the Cherry Servers API
1. returns the `cherry.cloud`, as it complies with [cloudprovider.Interface](https://godoc.org/k8s.io/cloud-provider#Interface)

The `cloudprovider` now has a functioning `struct` that can perform the CCM functionality.

### File Structure

The primary entrypoint to the Cherry Servers provider is in [cloud.go](./cherry/cloud.go). This file contains
the initialization functions, as above, as well as registers the Cherry Servers provider and sets it up.

The `cloud struct` itself is created via `newCloud()`, also in [cloud.go](./cherry/cloud.go). This
initializes the `struct` with the `cherrygo.Client`, as well as `struct`s for each of the sub-components
that are supported: [LoadBalancer](https://pkg.go.dev/k8s.io/cloud-provider#LoadBalancer) and [InstancesV2](https://pkg.go.dev/k8s.io/cloud-provider#InstancesV2). The specific logic for each of these is contained in its own file:

* `InstancesV2`: [devices.go](./cherry/devices.go)
* `LoadBalancer`: [loadbalancers.go](./cherry/loadbalancers.go)

The other calls to `cloud` return `nil`, indicating they are not supported.

### Adding Functionality

To add support for additional elements of [cloudprovider.Interface](https://godoc.org/k8s.io/cloud-provider#Interface)

1. Modify `newCloud()` in [cloud.go](./cherry/cloud.go) to populate the `cloud struct`, using `newX()`, e.g. to support `Routes()`, populate with `newRoutes`.
1. Create a file to support the new functionality, with the name of the file matching the functionality, e.g. for `Routes`, name the file `routes.go`.
1. In the new file:
   * Create a `type <functionality> struct` with at least the `client` and `project` properties, as well as any others required, e.g. `type routes struct`
   * Create a `func newX()` to create and populate the `struct`
   * Add necessary `func` with the correct receiver to implement the new functionality, per [cloudprovider.Interface](https://godoc.org/k8s.io/cloud-provider#Interface)
   * Create a test file for the new file, testing each functionality, e.g. `routes_test.go`. See the section below on testing.

### Testing

By definition, a cloud controller manager is intended to interface with a cloud provider. It would be difficult
and expensive to test the CCM against the true Cherry Servers API each time.

To simplify matters, the Cherry Servers CCM has a built-in Cherry Servers API server, which is used
during testing. It simulates a true Cherry Servers API server, and enables creating or removing resources without actually deploying anything.

The simulated backend is based around [httptest](https://pkg.go.dev/net/http/httptest), the go-provided
http server mock.

The simulated backend is available in the directory [cherry/server](./cherry/server).

All of the tests leverage `testGetValidCloud()` from
[cloud_test.go](./metal/cloud_test.go) to:

* launch a simulated Cherry Servers API server that will terminate at the end of tests
* create an instance of `cloud` that is configured to connect to the simulated API server
* create an instance of [store.Memory](./cherry/server/store) so you can manipulate the "backend data" that the API server returns

To run any test:

1. `vc, backend := testGetValidCloud(t)`
1. use `backend` to input the seed data you want
1. call the function under test
1. check the results, either as the return from the function under test, or as the modified data in the `backend`

For examples, see [devices_test.go](./cherry/devices_test.go) or [facilities_test.go](./cherry/facilities_test.go).

## Branches and Versioning

The Cherry Servers CCM is similar to the standard Kubernetes versioning and branching model.

* `main` is the main branch, and is the current development branch.
* `vX.Y` is the release branch for a particular Kubernetes `major.minor` version, e.g. `v1.27`. This branch is created when the first work is done for a particular Kubernetes `major.minor` release, and is updated with any patches for that `major.minor` stream.

`main` generally will be up to date with the most recent `vX.Y` branch, and will be the basis for the next `vX.Y+1` branch.

### Creating a new version

When a new Kubernetes `major.minor` version is released, create a new `vX.Y` branch from `main`.
This branch is used for all patches to that `major.minor` version.

### Patching a version

When a new patch is required for a `vX.Y` branch, apply the fix to the HEAD of the specific branch.

### Cutting a Release

To cut a release, tag a commit on the branch. The tag should be of the form `vX.Y.Z`,
e.g. `v1.27.0`. You should tag **only** on a commit in a branch whose name matches the
`major.minor` of the tag.

As of this writing, CI does not automatically verify that the tag matches the branch, so
be careful with tagging.
