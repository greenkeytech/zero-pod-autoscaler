# Zero Pod Autoscaler

Zero Pod Autoscaler (ZPA) manages a Deployment resource. It can scale
the Deployment all the way down to zero replicas when it is not in
use. It can work alongside an HPA: when scaled to zero, the HPA
ignores the Deployment; once scaled back to one, the HPA may scale up
further.

To do this, ZPA is a **TCP proxy** in front of the Service load balancing
a Deployment. This allows the ZPA to track the number of connections
and scale the Deployment to zero when the connection count has been
zero for some time period.

If scaled to zero, an incoming connection triggers a scale-to-one
action. Once Service's Endpoints include a "ready" address, the
connection can complete, hopefully before the client times out.

## Usage

Usage is awkward, certainly more difficult than using an HPA.

The ZPA points to an existing Deployment resource with a corresponding
Service resource and needs three main configuration parameters:

- name of the Deployment
- name of the Endpoints associated with the Service
- target address:port to which requests should be proxied (typically
  the Service name and port)

The ZPA then consists of another Deployment of the ZPA and another
Service selecting the ZPA pods. Instead of connecting to the original
Service, clients must connect to the ZPA Service resource.

Finally, the ZPA needs a service account with sufficient permissions
to watch Endpoints and scale Deployments.

``` yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myzpa
  labels:
    app.kubernetes.io/name: myzpa
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: myzpa
  template:
    metadata:
      labels:
        app.kubernetes.io/name: myzpa
    spec:
      serviceAccountName: zpa
      containers:
      - name: zpa
        image: greenkeytech/zero-pod-autoscaler:0.4.0
        imagePullPolicy: IfNotPresent
        args:
        - --namespace=$(NAMESPACE)
        - --address=0.0.0.0:80
        - --deployment=myapp
        - --endpoints=myapp
        - --target=myapp:80
        ports:
        - name: proxy
          protocol: TCP
          containerPort: 80

---

apiVersion: v1
kind: Service
metadata:
  name: myzpa
spec:
  type: ClusterIP
  ports:
  - name: http
    port: 80
    targetPort: proxy
    protocol: TCP
  selector:
    app.kubernetes.io/name: myzpa
    
---

apiVersion: v1
kind: ServiceAccount
metadata:
  name: zpa

---

kind: Role
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: read-deployments
rules:
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list", "watch"]

---

kind: Role
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: scale-deployments
rules:
- apiGroups: ["apps"]
  resources: ["deployments", "deployments/scale"]
  verbs: ["patch", "update"]

---

kind: Role
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: read-endpoints
rules:
- apiGroups: [""]
  resources: ["endpoints"]
  verbs: ["get", "list", "watch"]

---

kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: zpa-read-endpoints
subjects:
- kind: ServiceAccount
  name: zpa
roleRef:
  kind: Role
  name: read-endpoints
  apiGroup: rbac.authorization.k8s.io

---

kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: zpa-read-deployments
subjects:
- kind: ServiceAccount
  name: zpa
roleRef:
  kind: Role
  name: read-deployments
  apiGroup: rbac.authorization.k8s.io

---

kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: zpa-scale-deployments
subjects:
- kind: ServiceAccount
  name: zpa
roleRef:
  kind: Role
  name: scale-deployments
  apiGroup: rbac.authorization.k8s.io
```
