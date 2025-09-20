# Nodedrain
Nodedrain is a kubernetes operator, build for draining, rebooting and upgrading nodes with support for plugins hooking into this process.

## Roadmap

- [X] Drain
  - [X] Plugin architecture
    - [X] [Basic Interface definition](./api/plugins/)
    - [X] [Basic example implementation](./examples/plugin/example-plugin.go)
- [ ] Reboot
  - [X] Check reboot required (/var/run/reboot-required)
  - [X] Request reboot if required (Single node)
  - [ ] Reboot Controller (Automatic reboot when required, withing configured schedule)
- [ ] Upgrade
  - [ ] Upgrade node using requested image
  - [ ] Upgrade Controller (Rollout configuration handling (CRD))
    - [ ] Ensure control planes upgraded first
    - [ ] Detect version mismatch before upgrade

## Getting Started

### Helm Chart (WIP)

Helm can be found in [chart](./dist/chart)

## Local development

### Prerequisites
- make
- go version v1.24+
- docker with buildx
- kubectl
- minikube

### Run locally
For build and start local development using minikube run:
```bash
make minikube-deploy
```
That will:
- Build the project
- Start minikube (nodedrain profile) with cert-manager and image registry
- Start registry proxy container on port 5000
- Deploy manifests for a fully working environment

Make changes to code, and run `make minikube-deploy` again.

#### Stop/Cleanup
```bash
make minikube-cleanup
```

### Linting
```bash
make lint
```

### Test
```bash
make test
```

### Test E2E
```bash
make test-e2e
```

### Build and push image
```bash
make docker-build IMG_REGISTRY=localhost:5000 IMG_NAME_CONTROLLER=controller IMG_TAG=latest DOCKER_BUILD_OPTIONS=--push
```

### Sample config

```sh
kubectl apply -k config/samples/
```

## Contributing

Contributions are what make the open source community such an amazing place to learn, inspire, and create. Any contributions you make are **greatly appreciated**.

If you have a suggestion that would make this better, please fork the repo and create a pull request. You can also simply open an issue with the tag "enhancement".
Don't forget to give the project a star! Thanks again!

1. Fork the Project
2. Create your Branch (`git checkout -b amazing-feature`)
3. Commit your Changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the Branch (`git push origin amazing-feature`)
5. Open a Pull Request

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

