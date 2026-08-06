# relay-proxy

![Version: 1.55.1](https://img.shields.io/badge/Version-1.55.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v1.55.1](https://img.shields.io/badge/AppVersion-v1.55.1-informational?style=flat-square)

A Helm chart to deploy go-feature-flag-relay proxy into Kubernetes

## How to use the chart

Please replace the keys `relayproxy.config` in  the `Values.yaml` to fit
your configuration. This file will be stored as `configmap` in your cluster and
be mount as a volume for the `relay-proxy`.

After changing the working directory to `cmd/relayproxy/helm-charts/relay-proxy`,
run the below command:

```shell
helm install . --name-template=go-feature-flag-relay-proxy
```

It will install the chart in your cluster.

## Monitoring Port Configuration

The Helm chart supports an optional `monitoringPort` configuration that allows you to:

- **Separate monitoring traffic**: Expose a dedicated port for health checks and monitoring
- **Enhanced security**: Keep monitoring endpoints separate from application traffic
- **Flexible health checks**: Use the monitoring port for liveness and readiness probes

### Example with monitoringPort:

```yaml
relayproxy:
  config: |
    server:
      mode: http
      port: 1031
      monitoringPort: 1032
    pollingInterval: 1000
    startWithRetrieverError: false
    logLevel: debug
    retriever:
      kind: http
      url: https://example.com/flags.yaml
    exporter:
      kind: log
```

When `monitoringPort` is configured:
1. The monitoring port is exposed alongside the HTTP port
2. Health checks (`/health` endpoint) use the monitoring port
3. Both ports are accessible through the Kubernetes service

### Example without monitoringPort (default behavior):

```yaml
relayproxy:
  config: |
    server:
      mode: http
      port: 1031
      monitoringPort: 1032
    pollingInterval: 1000
    startWithRetrieverError: false
    logLevel: debug
    retriever:
      kind: http
      url: https://example.com/flags.yaml
    exporter:
      kind: log
```

## API Key Authentication (Vault-backed)

Set `apiKeyAuth.enabled=true` to require an API key on the relay proxy's
evaluation endpoints. Callers must then send an `Authorization: Bearer <key>`
header; `/health`, `/info` and `/metrics` stay unauthenticated so the liveness
and readiness probes (and any metrics scraper) keep working unchanged.

The key itself is never rendered into the chart, into `relayproxy.config`, or
into git. The chart creates a **VaultSecret** custom resource; the Vault secret
operator in the cluster reconciles it into a Kubernetes `Secret` of the same
name, and the deployment injects that Secret's entries into the container as
environment variables. The relay proxy reads `AUTHORIZEDKEYS_EVALUATION` and
`AUTHORIZEDKEYS_ADMIN` as comma-separated lists and overlays them onto
`authorizedKeys.evaluation` / `authorizedKeys.admin`, which is why the config
ConfigMap needs no change at all:

```
Vault  ──▶  VaultSecret  ──▶  Secret  ──▶  AUTHORIZEDKEYS_EVALUATION  ──▶  authorizedKeys.evaluation
           (this chart)      (operator)         (env var)                      (relay proxy config)
```

### Example

```yaml
apiKeyAuth:
  enabled: true
  vaultSecret:
    path: kv/goff/relay-proxy
```

This renders a `VaultSecret` named `<release>-relay-proxy-api-key` and injects
the Secret's `evaluation-api-key` entry as `AUTHORIZEDKEYS_EVALUATION`.

### Using a different Vault CRD

`apiKeyAuth.vaultSecret.apiVersion` / `.kind` and the free-form
`apiKeyAuth.vaultSecret.extraSpec` make the resource portable across operators.
For the HashiCorp Vault Secrets Operator:

```yaml
apiKeyAuth:
  enabled: true
  vaultSecret:
    apiVersion: secrets.hashicorp.com/v1beta1
    kind: VaultStaticSecret
    path: goff/relay-proxy
    extraSpec:
      mount: kv
      type: kv-v2
      vaultAuthRef: default
      refreshAfter: 60s
```

### Bringing your own Secret

Set `apiKeyAuth.vaultSecret.create=false` and point
`apiKeyAuth.vaultSecret.name` at an existing Secret to consume a key that
something else already manages. Either way the Secret must exist by the time the
pod starts, otherwise the pod sits in `CreateContainerConfigError`.

### Admin keys

`apiKeyAuth.adminSecretKey` is empty by default, so no admin key is injected.
Set it to a key of the same Secret to also protect the `/v1/admin` endpoints.

**Homepage:** <https://gofeatureflag.org>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| thomaspoignant | <thomas.poignant@gofeatureflag.org> | <https://gofeatureflag.org> |

## Source Code

* <https://github.com/thomaspoignant/go-feature-flag>

## Values

<table>
	<thead>
		<th>Key</th>
		<th>Type</th>
		<th>Default</th>
		<th>Description</th>
	</thead>
	<tbody>
		<tr>
			<td id="affinity">
				<a href="./values.yaml#L136">affinity</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Affinity settings for pod assignment to nodes
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth">
				<a href="./values.yaml#L170">apiKeyAuth</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{
  "adminEnvVar": "AUTHORIZEDKEYS_ADMIN",
  "adminSecretKey": "",
  "enabled": false,
  "evaluationEnvVar": "AUTHORIZEDKEYS_EVALUATION",
  "evaluationSecretKey": "evaluation-api-key",
  "vaultSecret": {
    "annotations": {},
    "apiVersion": "ricoberger.de/v1alpha1",
    "create": true,
    "extraSpec": {},
    "keys": [],
    "kind": "VaultSecret",
    "name": "",
    "path": "kv/goff/relay-proxy",
    "secretType": "Opaque"
  }
}
</pre>
</div>
			</td>
			<td>
				API key authentication for the relay proxy. The keys themselves never live in the chart or in git: a VaultSecret resource materialises a Kubernetes Secret, and the deployment injects that Secret's entries as environment variables. The relay proxy maps `AUTHORIZEDKEYS_EVALUATION` and `AUTHORIZEDKEYS_ADMIN` (comma-separated) onto `authorizedKeys.evaluation` and `authorizedKeys.admin`, so `relayproxy.config` needs no change.
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--adminEnvVar">
				<a href="./values.yaml#L184">apiKeyAuth.adminEnvVar</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"AUTHORIZEDKEYS_ADMIN"
</pre>
</div>
			</td>
			<td>
				Environment variable receiving the admin API keys
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--adminSecretKey">
				<a href="./values.yaml#L180">apiKeyAuth.adminSecretKey</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				Key inside the Secret holding the admin API keys (used by the `/v1/admin` endpoints). Set to "" to not inject an admin key.
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--enabled">
				<a href="./values.yaml#L174">apiKeyAuth.enabled</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
false
</pre>
</div>
			</td>
			<td>
				Enable API key authentication. Callers must then send an `Authorization: Bearer &lt;key&gt;` header on the evaluation endpoints; `/health`, `/info` and `/metrics` stay unauthenticated.
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--evaluationEnvVar">
				<a href="./values.yaml#L182">apiKeyAuth.evaluationEnvVar</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"AUTHORIZEDKEYS_EVALUATION"
</pre>
</div>
			</td>
			<td>
				Environment variable receiving the evaluation API keys
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--evaluationSecretKey">
				<a href="./values.yaml#L177">apiKeyAuth.evaluationSecretKey</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"evaluation-api-key"
</pre>
</div>
			</td>
			<td>
				Key inside the Secret holding the evaluation API keys (comma-separated for several). Set to "" to not inject an evaluation key.
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--annotations">
				<a href="./values.yaml#L198">apiKeyAuth.vaultSecret.annotations</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Annotations to add to the VaultSecret (accepts template)
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--apiVersion">
				<a href="./values.yaml#L191">apiKeyAuth.vaultSecret.apiVersion</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"ricoberger.de/v1alpha1"
</pre>
</div>
			</td>
			<td>
				apiVersion of the VaultSecret custom resource, so the chart works with whichever Vault secret CRD the cluster provides
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--create">
				<a href="./values.yaml#L188">apiKeyAuth.vaultSecret.create</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
true
</pre>
</div>
			</td>
			<td>
				Create the VaultSecret resource. Set to false to consume a Secret that already exists (or is produced by something else) under `name` below.
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--extraSpec">
				<a href="./values.yaml#L208">apiKeyAuth.vaultSecret.extraSpec</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Extra fields merged into the VaultSecret `spec`, for CRD-specific settings such as `vaultAuthRef`, `mount` or `refreshAfter` (accepts template)
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--keys">
				<a href="./values.yaml#L204">apiKeyAuth.vaultSecret.keys</a>
            </td>
			<td>
list
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
[]
</pre>
</div>
			</td>
			<td>
				Restrict which Vault keys are copied into the Secret; empty copies all
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--kind">
				<a href="./values.yaml#L193">apiKeyAuth.vaultSecret.kind</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"VaultSecret"
</pre>
</div>
			</td>
			<td>
				kind of the custom resource
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--name">
				<a href="./values.yaml#L196">apiKeyAuth.vaultSecret.name</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				Name of the VaultSecret and of the Secret it produces (accepts template). Defaults to `&lt;fullname&gt;-api-key`.
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--path">
				<a href="./values.yaml#L200">apiKeyAuth.vaultSecret.path</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"kv/goff/relay-proxy"
</pre>
</div>
			</td>
			<td>
				Path in Vault holding the API keys
			</td>
		</tr>
		<tr>
			<td id="apiKeyAuth--vaultSecret--secretType">
				<a href="./values.yaml#L202">apiKeyAuth.vaultSecret.secretType</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"Opaque"
</pre>
</div>
			</td>
			<td>
				Type of the Kubernetes Secret produced from the Vault path
			</td>
		</tr>
		<tr>
			<td id="autoscaling">
				<a href="./values.yaml#L117">autoscaling</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{
  "enabled": false,
  "maxReplicas": 100,
  "minReplicas": 1,
  "targetCPUUtilizationPercentage": 80,
  "targetMemoryUtilizationPercentage": 80
}
</pre>
</div>
			</td>
			<td>
				automatically scale the deployment up and down based on observed CPU and memory utilization
			</td>
		</tr>
		<tr>
			<td id="autoscaling--enabled">
				<a href="./values.yaml#L119">autoscaling.enabled</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
false
</pre>
</div>
			</td>
			<td>
				enable autoscaling
			</td>
		</tr>
		<tr>
			<td id="autoscaling--maxReplicas">
				<a href="./values.yaml#L123">autoscaling.maxReplicas</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
100
</pre>
</div>
			</td>
			<td>
				max replicas to scale to
			</td>
		</tr>
		<tr>
			<td id="autoscaling--minReplicas">
				<a href="./values.yaml#L121">autoscaling.minReplicas</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
1
</pre>
</div>
			</td>
			<td>
				min replicas to scale to
			</td>
		</tr>
		<tr>
			<td id="autoscaling--targetCPUUtilizationPercentage">
				<a href="./values.yaml#L125">autoscaling.targetCPUUtilizationPercentage</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
80
</pre>
</div>
			</td>
			<td>
				target CPU utilization percentage to spin up new pods
			</td>
		</tr>
		<tr>
			<td id="autoscaling--targetMemoryUtilizationPercentage">
				<a href="./values.yaml#L127">autoscaling.targetMemoryUtilizationPercentage</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
80
</pre>
</div>
			</td>
			<td>
				target memory utilization percentage to spin up new pods
			</td>
		</tr>
		<tr>
			<td id="commonLabels">
				<a href="./values.yaml#L65">commonLabels</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Additional labels to add to all resources
			</td>
		</tr>
		<tr>
			<td id="env">
				<a href="./values.yaml#L18">env</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Environment variables to pass to the relay proxy
			</td>
		</tr>
		<tr>
			<td id="extraManifests">
				<a href="./values.yaml#L139">extraManifests</a>
            </td>
			<td>
list
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
[]
</pre>
</div>
			</td>
			<td>
				Array of extra objects to deploy with the release (evaluated as a template)
			</td>
		</tr>
		<tr>
			<td id="fullnameOverride">
				<a href="./values.yaml#L46">fullnameOverride</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				Completely override the deployment name for kubernetes objects
			</td>
		</tr>
		<tr>
			<td id="image--fips">
				<a href="./values.yaml#L39">image.fips</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
false
</pre>
</div>
			</td>
			<td>
				Use the FIPS 140-3 validated image variant (appends the `-fips` suffix to the tag, available since v1.54.0)
			</td>
		</tr>
		<tr>
			<td id="image--pullPolicy">
				<a href="./values.yaml#L35">image.pullPolicy</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"IfNotPresent"
</pre>
</div>
			</td>
			<td>
				The image is pulled only if it is not already present locally
			</td>
		</tr>
		<tr>
			<td id="image--repository">
				<a href="./values.yaml#L33">image.repository</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"gofeatureflag/go-feature-flag"
</pre>
</div>
			</td>
			<td>
				The image repository to pull from
			</td>
		</tr>
		<tr>
			<td id="image--tag">
				<a href="./values.yaml#L37">image.tag</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				Overrides the image tag whose default is the chart appVersion
			</td>
		</tr>
		<tr>
			<td id="imagePullSecrets">
				<a href="./values.yaml#L42">imagePullSecrets</a>
            </td>
			<td>
list
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
[]
</pre>
</div>
			</td>
			<td>
				Specify imagePullSecrets to be used for the deployment
			</td>
		</tr>
		<tr>
			<td id="ingress">
				<a href="./values.yaml#L90">ingress</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{
  "annotations": {},
  "className": "",
  "enabled": false,
  "hosts": [
    {
      "host": "chart-example.local",
      "paths": [
        {
          "path": "/",
          "pathType": "ImplementationSpecific"
        }
      ]
    }
  ],
  "tls": []
}
</pre>
</div>
			</td>
			<td>
				Ingress configuration
			</td>
		</tr>
		<tr>
			<td id="ingress--annotations">
				<a href="./values.yaml#L96">ingress.annotations</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Annotations to add to the ingress
			</td>
		</tr>
		<tr>
			<td id="ingress--className">
				<a href="./values.yaml#L94">ingress.className</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				Ingress class name
			</td>
		</tr>
		<tr>
			<td id="ingress--enabled">
				<a href="./values.yaml#L92">ingress.enabled</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
false
</pre>
</div>
			</td>
			<td>
				Enable ingress
			</td>
		</tr>
		<tr>
			<td id="nameOverride">
				<a href="./values.yaml#L44">nameOverride</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				replaces the name of the chart in the Chart.yaml file
			</td>
		</tr>
		<tr>
			<td id="nodeSelector">
				<a href="./values.yaml#L130">nodeSelector</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Node labels for pod assignment
			</td>
		</tr>
		<tr>
			<td id="pdb--enable">
				<a href="./values.yaml#L58">pdb.enable</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
false
</pre>
</div>
			</td>
			<td>
				
			</td>
		</tr>
		<tr>
			<td id="pdb--minAvailable">
				<a href="./values.yaml#L59">pdb.minAvailable</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
1
</pre>
</div>
			</td>
			<td>
				
			</td>
		</tr>
		<tr>
			<td id="podAnnotations">
				<a href="./values.yaml#L62">podAnnotations</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Pod annotations to add to the deployment
			</td>
		</tr>
		<tr>
			<td id="podLabels">
				<a href="./values.yaml#L68">podLabels</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Additional labels to add to pods
			</td>
		</tr>
		<tr>
			<td id="podSecurityContext">
				<a href="./values.yaml#L71">podSecurityContext</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				A security context defines privilege and access control settings for a Pod
			</td>
		</tr>
		<tr>
			<td id="relayproxy--config">
				<a href="./values.yaml#L3">relayproxy.config</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"server:\n  mode: http\n  port: 1031\npollingInterval: 1000\nstartWithRetrieverError: false\nlogLevel: info\nretriever:\n  kind: http\n  url: https://raw.githubusercontent.com/thomaspoignant/go-feature-flag/main/examples/retriever_file/flags.goff.yaml\nexporter:\n  kind: log\nenableSwagger: true\n"
</pre>
</div>
			</td>
			<td>
				GO Feature Flag relay proxy configuration as string (accept template). If monitoringPort is specified in the config (either as "monitoringPort: 1032" or "server.monitoringPort: 1032"), it will be exposed as a separate port and used for liveness and readiness probes instead of the main HTTP port. Example: add "monitoringPort: 1032" or "server.monitoringPort: 1032" to your config to enable separate monitoring port.
			</td>
		</tr>
		<tr>
			<td id="replicaCount">
				<a href="./values.yaml#L29">replicaCount</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
1
</pre>
</div>
			</td>
			<td>
				The number of replicas to create for the deployment
			</td>
		</tr>
		<tr>
			<td id="resources--requests--cpu">
				<a href="./values.yaml#L114">resources.requests.cpu</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"500m"
</pre>
</div>
			</td>
			<td>
				The amount of cpu to request for the container
			</td>
		</tr>
		<tr>
			<td id="resources--requests--memory">
				<a href="./values.yaml#L112">resources.requests.memory</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"128Mi"
</pre>
</div>
			</td>
			<td>
				The amount of memory to request for the container
			</td>
		</tr>
		<tr>
			<td id="securityContext">
				<a href="./values.yaml#L75">securityContext</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				A security context defines privilege and access control settings for a Container
			</td>
		</tr>
		<tr>
			<td id="service--port">
				<a href="./values.yaml#L87">service.port</a>
            </td>
			<td>
int
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
1031
</pre>
</div>
			</td>
			<td>
				The port to expose on the service
			</td>
		</tr>
		<tr>
			<td id="service--type">
				<a href="./values.yaml#L85">service.type</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
"ClusterIP"
</pre>
</div>
			</td>
			<td>
				The type of service to create
			</td>
		</tr>
		<tr>
			<td id="serviceAccount--annotations">
				<a href="./values.yaml#L52">serviceAccount.annotations</a>
            </td>
			<td>
object
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
{}
</pre>
</div>
			</td>
			<td>
				Annotations to add to the service account
			</td>
		</tr>
		<tr>
			<td id="serviceAccount--create">
				<a href="./values.yaml#L50">serviceAccount.create</a>
            </td>
			<td>
bool
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
true
</pre>
</div>
			</td>
			<td>
				Specifies whether a service account should be created
			</td>
		</tr>
		<tr>
			<td id="serviceAccount--name">
				<a href="./values.yaml#L55">serviceAccount.name</a>
            </td>
			<td>
string
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
""
</pre>
</div>
			</td>
			<td>
				The name of the service account to use. If not set and create is true, a name is generated using the fullname template
			</td>
		</tr>
		<tr>
			<td id="tolerations">
				<a href="./values.yaml#L133">tolerations</a>
            </td>
			<td>
list
</td>
			<td>
				<div style="max-width: 300px;">
<pre lang="json">
[]
</pre>
</div>
			</td>
			<td>
				Tolerations for pod assignment
			</td>
		</tr>
	</tbody>
</table>

## Advanced
You can edit the `values.yaml` file to enable an ingress or the autoscaling.
